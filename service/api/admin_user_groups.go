package api

import (
	"errors"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	E "github.com/sagernet/sing/common/exceptions"

	"github.com/go-chi/chi/v5"
)

type adminUserGroupView struct {
	Name            string           `json:"name"`
	Memberships     []adminUserView  `json:"memberships"`
	Revisions       map[string]int64 `json:"revisions"`
	SubscriptionURL string           `json:"subscription_url,omitempty"`
}

type adminUserGroupInput struct {
	Name        string                          `json:"name"`
	Memberships []adminUserGroupMembershipInput `json:"memberships"`
	Revisions   map[string]int64                `json:"revisions"`
}

type adminUserGroupMembershipInput struct {
	ID         string `json:"id,omitempty"`
	Inbound    string `json:"inbound"`
	UUID       string `json:"uuid"`
	Password   string `json:"password"`
	Flow       string `json:"flow"`
	AlterID    int    `json:"alter_id"`
	Enabled    *bool  `json:"enabled"`
	QuotaBytes int64  `json:"quota_bytes"`
	ExpiresAt  int64  `json:"expires_at"`
	MaxIPs     *int   `json:"max_ips"`
}

type adminUserGroupRevisionInput struct {
	Revisions map[string]int64 `json:"revisions"`
}

func (input adminUserGroupMembershipInput) userInput(name string, revision int64) adminUserInput {
	return adminUserInput{
		Inbound: input.Inbound, Name: name, UUID: input.UUID, Password: input.Password,
		Flow: input.Flow, AlterID: input.AlterID, Enabled: input.Enabled,
		QuotaBytes: input.QuotaBytes, ExpiresAt: input.ExpiresAt, MaxIPs: input.MaxIPs,
		Revision: revision,
	}
}

func (a *adminAPI) getUserGroup(writer http.ResponseWriter, request *http.Request) {
	name := requestedUserGroupName(request)
	active := a.activeUsage()
	a.storeAccess.RLock()
	view, loaded := a.userGroupViewLocked(name, active)
	a.storeAccess.RUnlock()
	if !loaded {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	writeAdminJSON(writer, http.StatusOK, view)
}

func (a *adminAPI) createUserGroup(writer http.ResponseWriter, request *http.Request) {
	var input adminUserGroupInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	a.mutateUserGroup(writer, "", input, true)
}

func (a *adminAPI) updateUserGroup(writer http.ResponseWriter, request *http.Request) {
	var input adminUserGroupInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	a.mutateUserGroup(writer, requestedUserGroupName(request), input, false)
}

func (a *adminAPI) mutateUserGroup(writer http.ResponseWriter, oldName string, input adminUserGroupInput, creating bool) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 128 {
		writeAdminError(writer, http.StatusBadRequest, "用戶名稱不能留空且不可超過 128 個字元")
		return
	}
	if len(input.Memberships) == 0 {
		writeAdminError(writer, http.StatusBadRequest, "用戶至少需要一個節點")
		return
	}

	normalized := make(map[string]adminUserInput, len(input.Memberships))
	memberIDs := make(map[string]string, len(input.Memberships))
	for _, membership := range input.Memberships {
		if membership.Inbound == "" {
			writeAdminError(writer, http.StatusBadRequest, "節點不能留空")
			return
		}
		if _, exists := normalized[membership.Inbound]; exists {
			writeAdminError(writer, http.StatusBadRequest, "同一用戶不可重複選擇節點")
			return
		}
		runtimeInbound := a.runtimes[membership.Inbound]
		if runtimeInbound == nil || runtimeInbound.Manager == nil {
			writeAdminError(writer, http.StatusBadRequest, "節點不支援動態用戶管理")
			return
		}
		value, err := normalizeAdminInput(membership.userInput(name, input.Revisions[membership.Inbound]), runtimeInbound.Manager.service.ManagedUserSchema())
		if err != nil {
			writeAdminError(writer, http.StatusBadRequest, err.Error())
			return
		}
		normalized[membership.Inbound] = value
		memberIDs[membership.Inbound] = membership.ID
	}

	a.mutation.Lock()
	defer a.unlockMutation()

	lookupName := oldName
	if creating {
		lookupName = name
	}
	current := a.userGroupMembers(lookupName)
	if creating && len(current) > 0 {
		writeAdminError(writer, http.StatusConflict, "用戶名稱已存在")
		return
	}
	if !creating && len(current) == 0 {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	if !creating && oldName != name && len(a.userGroupMembers(name)) > 0 {
		writeAdminError(writer, http.StatusConflict, "用戶名稱已存在")
		return
	}

	affectedSet := make(map[string]bool, len(current)+len(normalized))
	for tag := range current {
		affectedSet[tag] = true
	}
	for tag := range normalized {
		affectedSet[tag] = true
	}
	tags := make([]string, 0, len(affectedSet))
	for tag := range affectedSet {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		if input.Revisions[tag] <= 0 {
			writeAdminError(writer, http.StatusConflict, "缺少節點 revision，請重新整理後再試")
			return
		}
	}

	if !creating {
		a.trafficAccess.Lock()
		for tag := range current {
			a.baselineUserTrafficLocked(tag, oldName, true)
		}
		a.trafficAccess.Unlock()
		current = a.userGroupMembers(oldName)
	}

	previous := make(map[string]*adminInboundStore, len(tags))
	var oldSubscription, newSubscription, oldExternalSubscription, newExternalSubscription string
	var hadOldSubscription, hadNewSubscription, hadOldExternalSubscription, hadNewExternalSubscription bool
	a.storeAccess.Lock()
	for _, tag := range tags {
		record := a.store.Inbounds[tag]
		if record == nil || record.Revision != input.Revisions[tag] {
			a.storeAccess.Unlock()
			writeAdminError(writer, http.StatusConflict, "資料已被其他操作更新，請重新整理後再試")
			return
		}
		previous[tag] = cloneInboundStore(record)
	}
	now := time.Now().UnixMilli()
	for _, tag := range tags {
		updated := cloneInboundStore(a.store.Inbounds[tag])
		filtered := updated.Users[:0]
		for _, user := range updated.Users {
			if existing := current[tag]; existing != nil && user.ID == existing.ID {
				continue
			}
			filtered = append(filtered, user)
		}
		updated.Users = filtered
		if value, desired := normalized[tag]; desired {
			existing := current[tag]
			requestedID := memberIDs[tag]
			if requestedID != "" && (existing == nil || requestedID != existing.ID) {
				a.storeAccess.Unlock()
				writeAdminError(writer, http.StatusConflict, "用戶節點身分已變更，請重新整理後再試")
				return
			}
			candidate := &adminUser{Inbound: tag, Type: a.runtimes[tag].Type, Name: name, Enabled: true, CreatedAt: now}
			if existing != nil {
				copyUser := *existing
				candidate = &copyUser
			} else {
				id, err := newAdminID()
				if err != nil {
					a.storeAccess.Unlock()
					writeAdminError(writer, http.StatusInternalServerError, err.Error())
					return
				}
				candidate.ID = id
			}
			candidate.Name = name
			candidate.UUID = value.UUID
			candidate.Password = value.Password
			candidate.Flow = value.Flow
			candidate.AlterID = value.AlterID
			candidate.QuotaBytes = value.QuotaBytes
			candidate.ExpiresAt = value.ExpiresAt
			if value.Enabled != nil {
				candidate.Enabled = *value.Enabled
			}
			if value.MaxIPs != nil {
				candidate.MaxIPs = *value.MaxIPs
			}
			candidate.UpdatedAt = now
			if err := validateUniqueUser(updated, candidate, ""); err != nil {
				a.storeAccess.Unlock()
				writeAdminError(writer, http.StatusConflict, err.Error())
				return
			}
			updated.Users = append(updated.Users, candidate)
		}
		updated.Authoritative = true
		updated.Revision = nextAdminRevision(updated.Revision, now)
		a.store.Inbounds[tag] = updated
	}
	if !creating && oldName != name {
		oldSubscription, hadOldSubscription = a.store.Subscriptions[oldName]
		newSubscription, hadNewSubscription = a.store.Subscriptions[name]
		oldExternalSubscription, hadOldExternalSubscription = a.store.ExternalSubscriptions[oldName]
		newExternalSubscription, hadNewExternalSubscription = a.store.ExternalSubscriptions[name]
		if hadOldSubscription {
			a.store.Subscriptions[name] = oldSubscription
			delete(a.store.Subscriptions, oldName)
		}
		if hadOldExternalSubscription {
			a.store.ExternalSubscriptions[name] = oldExternalSubscription
			delete(a.store.ExternalSubscriptions, oldName)
		}
	}
	a.storeAccess.Unlock()

	if err := a.commitInboundBatch(tags, previous); err != nil {
		if !creating && oldName != name {
			a.storeAccess.Lock()
			restoreStringMapEntry(a.store.Subscriptions, oldName, oldSubscription, hadOldSubscription)
			restoreStringMapEntry(a.store.Subscriptions, name, newSubscription, hadNewSubscription)
			restoreStringMapEntry(a.store.ExternalSubscriptions, oldName, oldExternalSubscription, hadOldExternalSubscription)
			restoreStringMapEntry(a.store.ExternalSubscriptions, name, newExternalSubscription, hadNewExternalSubscription)
			a.storeAccess.Unlock()
			_ = a.saveStore()
		}
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	if !creating && oldName != name {
		a.storeAccess.Lock()
		if a.userAliases == nil {
			a.userAliases = make(map[string]adminManagedUserIdentity)
		}
		for tag, user := range current {
			a.userAliases[adminUserKey(tag, oldName)] = adminManagedUserIdentity{ID: user.ID, Generation: user.TrafficGeneration}
		}
		a.storeAccess.Unlock()
	}
	active := a.activeUsage()
	a.storeAccess.RLock()
	view, _ := a.userGroupViewLocked(name, active)
	a.storeAccess.RUnlock()
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	writeAdminJSON(writer, status, view)
}

func (a *adminAPI) deleteUserGroup(writer http.ResponseWriter, request *http.Request) {
	var input adminUserGroupRevisionInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	name := requestedUserGroupName(request)
	a.mutation.Lock()
	defer a.unlockMutation()
	current := a.userGroupMembers(name)
	if len(current) == 0 {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	tags := make([]string, 0, len(current))
	for tag := range current {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	previous := make(map[string]*adminInboundStore, len(tags))
	a.storeAccess.Lock()
	for _, tag := range tags {
		record := a.store.Inbounds[tag]
		if record == nil || input.Revisions[tag] <= 0 || record.Revision != input.Revisions[tag] {
			a.storeAccess.Unlock()
			writeAdminError(writer, http.StatusConflict, "資料已被其他操作更新，請重新整理後再試")
			return
		}
		previous[tag] = cloneInboundStore(record)
		updated := cloneInboundStore(record)
		users := updated.Users[:0]
		for _, user := range updated.Users {
			if user.ID != current[tag].ID {
				users = append(users, user)
			}
		}
		updated.Users = users
		updated.Authoritative = true
		updated.Revision = nextAdminRevision(updated.Revision, time.Now().UnixMilli())
		a.store.Inbounds[tag] = updated
	}
	a.storeAccess.Unlock()
	if err := a.commitInboundBatch(tags, previous); err != nil {
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	for tag, user := range current {
		a.trafficAccess.Lock()
		a.baselineUserTrafficForIdentityLocked(tag, name, user.ID, user.TrafficGeneration, false)
		a.trafficAccess.Unlock()
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) resetUserGroupTraffic(writer http.ResponseWriter, request *http.Request) {
	name := requestedUserGroupName(request)
	a.mutation.Lock()
	defer a.unlockMutation()
	current := a.userGroupMembers(name)
	if len(current) == 0 {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	previous := make(map[string]*adminInboundStore, len(current))
	a.storeAccess.Lock()
	for tag, target := range current {
		record := a.store.Inbounds[tag]
		previous[tag] = cloneInboundStore(record)
		for _, user := range record.Users {
			if user.ID == target.ID {
				user.UploadBytes = 0
				user.DownloadBytes = 0
				user.TrafficGeneration++
				user.UpdatedAt = time.Now().UnixMilli()
				break
			}
		}
	}
	a.storeAccess.Unlock()
	if err := a.saveStore(); err != nil {
		a.storeAccess.Lock()
		maps.Copy(a.store.Inbounds, previous)
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusInternalServerError, E.Cause(err, "儲存流量資料失敗").Error())
		return
	}
	for tag := range current {
		a.trafficAccess.Lock()
		a.baselineUserTrafficLocked(tag, name, false)
		a.trafficAccess.Unlock()
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) userGroupMembers(name string) map[string]*adminUser {
	result := make(map[string]*adminUser)
	a.storeAccess.RLock()
	defer a.storeAccess.RUnlock()
	for tag, record := range a.store.Inbounds {
		if runtimeInbound := a.runtimes[tag]; runtimeInbound == nil || runtimeInbound.Manager == nil {
			continue
		}
		for _, user := range record.Users {
			if user.Name == name {
				copyUser := *user
				result[tag] = &copyUser
				break
			}
		}
	}
	return result
}

func (a *adminAPI) userGroupViewLocked(name string, active map[string]adminUsage) (adminUserGroupView, bool) {
	view := adminUserGroupView{Name: name, Revisions: make(map[string]int64)}
	for tag, record := range a.store.Inbounds {
		if runtimeInbound := a.runtimes[tag]; runtimeInbound == nil || runtimeInbound.Manager == nil {
			continue
		}
		for _, user := range record.Users {
			if user.Name != name {
				continue
			}
			view.Memberships = append(view.Memberships, makeAdminUserView(user, record, active[adminUserKey(tag, name)], true))
			view.Revisions[tag] = record.Revision
		}
	}
	if len(view.Memberships) == 0 {
		return view, false
	}
	sort.Slice(view.Memberships, func(i, j int) bool { return view.Memberships[i].Inbound < view.Memberships[j].Inbound })
	if externalID := a.store.ExternalSubscriptions[name]; validExternalSubscriptionID(externalID) && a.publicBaseURL != "" {
		view.SubscriptionURL = a.publicBaseURL + "/sub/" + url.PathEscape(externalID)
	} else if token := a.store.Subscriptions[name]; token != "" && a.publicBaseURL != "" && len(a.subscriptionLinksLocked(name, time.Now().UnixMilli(), active)) > 0 {
		view.SubscriptionURL = a.publicBaseURL + subscriptionRoutePrefix + token
	}
	return view, true
}

func (a *adminAPI) commitInboundBatch(tags []string, previous map[string]*adminInboundStore) error {
	for _, tag := range tags {
		if err := a.applyInbound(tag, true); err != nil {
			return errors.Join(E.Cause(err, "更新核心用戶失敗"), a.rollbackInboundBatch(tags, previous, false))
		}
	}
	if err := a.saveStore(); err != nil {
		return errors.Join(E.Cause(err, "儲存用戶資料失敗"), a.rollbackInboundBatch(tags, previous, true))
	}
	return nil
}

func (a *adminAPI) rollbackInboundBatch(tags []string, previous map[string]*adminInboundStore, persist bool) error {
	a.storeAccess.Lock()
	maps.Copy(a.store.Inbounds, previous)
	a.storeAccess.Unlock()
	var rollbackErr error
	for _, tag := range tags {
		rollbackErr = errors.Join(rollbackErr, a.applyInbound(tag, true))
	}
	if persist {
		rollbackErr = errors.Join(rollbackErr, a.saveStore())
	}
	return rollbackErr
}

func restoreStringMapEntry(values map[string]string, key string, value string, loaded bool) {
	if loaded {
		values[key] = value
	} else {
		delete(values, key)
	}
}

func requestedUserGroupName(request *http.Request) string {
	if name := request.URL.Query().Get("name"); name != "" {
		return name
	}
	return chi.URLParam(request, "name")
}
