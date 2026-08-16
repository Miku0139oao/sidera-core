package api

import (
	"errors"
	"maps"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	E "github.com/sagernet/sing/common/exceptions"

	"github.com/go-chi/chi/v5"
)

type adminUserGroupView struct {
	Name            string            `json:"name"`
	Account         *adminAccountView `json:"account,omitempty"`
	Memberships     []adminUserView   `json:"memberships"`
	Revisions       map[string]int64  `json:"revisions"`
	SubscriptionURL string            `json:"subscription_url,omitempty"`
}

type adminUserGroupInput struct {
	Name            string                          `json:"name"`
	PolicyScope     string                          `json:"policy_scope"`
	AccountRevision int64                           `json:"account_revision"`
	Enabled         *bool                           `json:"enabled"`
	QuotaBytes      int64                           `json:"quota_bytes"`
	ExpiresAt       int64                           `json:"expires_at"`
	MaxIPs          *int                            `json:"max_ips"`
	ResetDays       int                             `json:"reset_days"`
	Memberships     []adminUserGroupMembershipInput `json:"memberships"`
	Revisions       map[string]int64                `json:"revisions"`
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
	AccountRevision int64            `json:"account_revision"`
	Revisions       map[string]int64 `json:"revisions"`
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
	if input.PolicyScope != "" && input.PolicyScope != adminAccountPolicyMembership && input.PolicyScope != adminAccountPolicyGlobal {
		writeAdminError(writer, http.StatusBadRequest, "帳戶政策範圍不正確")
		return
	}
	if input.QuotaBytes < 0 || input.ExpiresAt < 0 || input.ResetDays < 0 || int64(input.ResetDays) > math.MaxInt64/adminDayMilliseconds || input.MaxIPs != nil && *input.MaxIPs < 0 {
		writeAdminError(writer, http.StatusBadRequest, "帳戶額度、到期時間、重設週期與 IP 限制不可為負數")
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

	accountID := ""
	var currentAccount *adminAccount
	if !creating {
		for _, user := range current {
			if accountID == "" {
				accountID = user.AccountID
			} else if accountID != user.AccountID {
				writeAdminError(writer, http.StatusConflict, "用戶節點未連結至同一帳戶，請先修復資料")
				return
			}
		}
		a.storeAccess.RLock()
		if account := a.store.Accounts[accountID]; account != nil {
			copyAccount := *account
			currentAccount = &copyAccount
		}
		a.storeAccess.RUnlock()
		if currentAccount == nil {
			writeAdminError(writer, http.StatusConflict, "用戶帳戶資料不存在，請重新整理後再試")
			return
		}
		if input.AccountRevision <= 0 || currentAccount.Revision != input.AccountRevision {
			writeAdminError(writer, http.StatusConflict, "帳戶資料已被其他操作更新，請重新整理後再試")
			return
		}
		if input.PolicyScope == "" {
			input.PolicyScope = currentAccount.PolicyScope
		}
		if input.PolicyScope != currentAccount.PolicyScope {
			writeAdminError(writer, http.StatusBadRequest, "既有帳戶不可變更政策範圍")
			return
		}
	} else {
		if input.PolicyScope == "" {
			input.PolicyScope = adminAccountPolicyGlobal
		}
		var err error
		accountID, err = newAdminID()
		if err != nil {
			writeAdminError(writer, http.StatusInternalServerError, err.Error())
			return
		}
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
	a.storeAccess.Lock()
	if a.store.Accounts == nil {
		a.store.Accounts = make(map[string]*adminAccount)
	}
	for identifier, account := range a.store.Accounts {
		if identifier != accountID && account != nil && strings.EqualFold(account.Name, name) {
			a.storeAccess.Unlock()
			writeAdminError(writer, http.StatusConflict, "用戶名稱已存在")
			return
		}
	}
	for _, tag := range tags {
		record := a.store.Inbounds[tag]
		if record == nil || record.Revision != input.Revisions[tag] {
			a.storeAccess.Unlock()
			writeAdminError(writer, http.StatusConflict, "資料已被其他操作更新，請重新整理後再試")
			return
		}
		previous[tag] = cloneInboundStore(record)
	}
	previousAccounts := cloneAdminAccounts(a.store.Accounts)
	previousSubscriptions := maps.Clone(a.store.Subscriptions)
	previousExternalSubscriptions := maps.Clone(a.store.ExternalSubscriptions)
	restorePreparedState := func() {
		maps.Copy(a.store.Inbounds, previous)
		a.store.Accounts = previousAccounts
		a.store.Subscriptions = previousSubscriptions
		a.store.ExternalSubscriptions = previousExternalSubscriptions
	}
	now := time.Now().UnixMilli()
	account := a.store.Accounts[accountID]
	if creating {
		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		account = &adminAccount{
			ID: accountID, Name: name, PolicyScope: input.PolicyScope, Enabled: enabled,
			Revision: nextAdminRevision(0, now), CreatedAt: now, UpdatedAt: now,
		}
		a.store.Accounts[accountID] = account
	} else if account == nil || account.Revision != input.AccountRevision {
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusConflict, "帳戶資料已被其他操作更新，請重新整理後再試")
		return
	}
	account.Name = name
	if account.PolicyScope == adminAccountPolicyGlobal {
		if input.Enabled != nil {
			account.Enabled = *input.Enabled
		}
		account.QuotaBytes = input.QuotaBytes
		account.ExpiresAt = input.ExpiresAt
		if input.MaxIPs != nil {
			account.MaxIPs = *input.MaxIPs
		}
		account.ResetDays = input.ResetDays
		for tag, user := range current {
			if _, retained := normalized[tag]; retained {
				continue
			}
			account.BaseUploadBytes = saturatingAdminTrafficAdd(account.BaseUploadBytes, user.UploadBytes)
			account.BaseDownloadBytes = saturatingAdminTrafficAdd(account.BaseDownloadBytes, user.DownloadBytes)
		}
	}
	if !creating {
		account.Revision = nextAdminRevision(account.Revision, now)
		account.UpdatedAt = now
	}
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
				restorePreparedState()
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
					restorePreparedState()
					a.storeAccess.Unlock()
					writeAdminError(writer, http.StatusInternalServerError, err.Error())
					return
				}
				candidate.ID = id
			}
			candidate.AccountID = accountID
			candidate.Name = name
			candidate.UUID = value.UUID
			candidate.Password = value.Password
			candidate.Flow = value.Flow
			candidate.AlterID = value.AlterID
			if account.PolicyScope == adminAccountPolicyGlobal {
				candidate.QuotaBytes = 0
				candidate.ExpiresAt = 0
				candidate.MaxIPs = 0
			} else {
				candidate.QuotaBytes = value.QuotaBytes
				candidate.ExpiresAt = value.ExpiresAt
				if value.MaxIPs != nil {
					candidate.MaxIPs = *value.MaxIPs
				}
			}
			if value.Enabled != nil {
				candidate.Enabled = *value.Enabled
			}
			candidate.UpdatedAt = now
			if err := validateUniqueUser(updated, candidate, ""); err != nil {
				restorePreparedState()
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
		if subscription, loaded := a.store.Subscriptions[oldName]; loaded {
			a.store.Subscriptions[name] = subscription
			delete(a.store.Subscriptions, oldName)
		}
		if subscription, loaded := a.store.ExternalSubscriptions[oldName]; loaded {
			a.store.ExternalSubscriptions[name] = subscription
			delete(a.store.ExternalSubscriptions, oldName)
		}
	}
	a.storeAccess.Unlock()

	if err := a.commitInboundBatch(tags, previous); err != nil {
		restoreErr := a.restoreUserGroupState(tags, previousAccounts, previousSubscriptions, previousExternalSubscriptions)
		writeAdminError(writer, http.StatusInternalServerError, errors.Join(err, restoreErr).Error())
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
	accountID := ""
	for tag := range current {
		tags = append(tags, tag)
		if accountID == "" {
			accountID = current[tag].AccountID
		} else if accountID != current[tag].AccountID {
			writeAdminError(writer, http.StatusConflict, "用戶節點未連結至同一帳戶，請先修復資料")
			return
		}
	}
	sort.Strings(tags)
	previous := make(map[string]*adminInboundStore, len(tags))
	a.storeAccess.Lock()
	account := a.store.Accounts[accountID]
	if account == nil || input.AccountRevision <= 0 || account.Revision != input.AccountRevision {
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusConflict, "帳戶資料已被其他操作更新，請重新整理後再試")
		return
	}
	previousAccounts := cloneAdminAccounts(a.store.Accounts)
	previousSubscriptions := maps.Clone(a.store.Subscriptions)
	previousExternalSubscriptions := maps.Clone(a.store.ExternalSubscriptions)
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
	delete(a.store.Subscriptions, name)
	delete(a.store.ExternalSubscriptions, name)
	a.storeAccess.Unlock()
	if err := a.commitInboundBatch(tags, previous); err != nil {
		restoreErr := a.restoreUserGroupState(tags, previousAccounts, previousSubscriptions, previousExternalSubscriptions)
		writeAdminError(writer, http.StatusInternalServerError, errors.Join(err, restoreErr).Error())
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
	accountID := ""
	for _, user := range current {
		if accountID == "" {
			accountID = user.AccountID
		} else if accountID != user.AccountID {
			writeAdminError(writer, http.StatusConflict, "用戶節點未連結至同一帳戶，請先修復資料")
			return
		}
	}
	a.storeAccess.Lock()
	previousAccounts := cloneAdminAccounts(a.store.Accounts)
	if account := a.store.Accounts[accountID]; account != nil && account.PolicyScope == adminAccountPolicyGlobal {
		account.BaseUploadBytes = 0
		account.BaseDownloadBytes = 0
		account.UpdatedAt = time.Now().UnixMilli()
	}
	for tag, target := range current {
		record := a.store.Inbounds[tag]
		previous[tag] = cloneInboundStore(record)
		updated := cloneInboundStore(record)
		for _, user := range updated.Users {
			if user.ID == target.ID {
				user.UploadBytes = 0
				user.DownloadBytes = 0
				user.TrafficGeneration++
				user.UpdatedAt = time.Now().UnixMilli()
				break
			}
		}
		a.store.Inbounds[tag] = updated
	}
	a.storeAccess.Unlock()
	tags := make([]string, 0, len(previous))
	for tag := range previous {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	a.trafficAccess.Lock()
	previousTrafficBaselines := maps.Clone(a.trafficBaselines)
	for tag := range current {
		a.baselineUserTrafficLocked(tag, name, false)
	}
	a.trafficAccess.Unlock()
	if err := a.commitInboundBatch(tags, previous); err != nil {
		a.storeAccess.Lock()
		a.store.Accounts = previousAccounts
		a.storeAccess.Unlock()
		a.trafficAccess.Lock()
		a.trafficBaselines = previousTrafficBaselines
		a.trafficAccess.Unlock()
		var restoreErr error
		for _, tag := range tags {
			restoreErr = errors.Join(restoreErr, a.applyInbound(tag, true))
		}
		restoreErr = errors.Join(restoreErr, a.saveStore())
		writeAdminError(writer, http.StatusInternalServerError, errors.Join(E.Cause(err, "儲存流量資料失敗"), restoreErr).Error())
		return
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
	accountUsage := a.allAccountUsageLocked(active)
	accountID := ""
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
			if accountID == "" {
				accountID = user.AccountID
			}
		}
	}
	if len(view.Memberships) == 0 {
		return view, false
	}
	if account := a.store.Accounts[accountID]; account != nil {
		accountView := makeAdminAccountView(account, accountUsage[accountID])
		view.Account = &accountView
	}
	sort.Slice(view.Memberships, func(i, j int) bool { return view.Memberships[i].Inbound < view.Memberships[j].Inbound })
	view.SubscriptionURL = a.subscriptionURLWithAccountsLocked(name, active, accountUsage)
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

func (a *adminAPI) restoreUserGroupState(tags []string, accounts map[string]*adminAccount, subscriptions map[string]string, externalSubscriptions map[string]string) error {
	a.storeAccess.Lock()
	a.store.Accounts = accounts
	a.store.Subscriptions = subscriptions
	a.store.ExternalSubscriptions = externalSubscriptions
	a.storeAccess.Unlock()
	var restoreErr error
	for _, tag := range tags {
		restoreErr = errors.Join(restoreErr, a.applyInbound(tag, true))
	}
	return errors.Join(restoreErr, a.saveStore())
}

func requestedUserGroupName(request *http.Request) string {
	if name := request.URL.Query().Get("name"); name != "" {
		return name
	}
	return chi.URLParam(request, "name")
}
