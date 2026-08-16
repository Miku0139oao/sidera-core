//go:build with_3xui_import

package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	C "github.com/Miku0139oao/sidera-core/constant"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
)

const (
	admin3XUIImportMaxDatabaseSize = 256 << 20
	admin3XUIImportMaxMappingSize  = 1 << 20
	admin3XUIImportReadTimeout     = 5 * time.Minute
)

const admin3XUIImportAvailable = true

var admin3XUIImportGate = make(chan struct{}, 1)

func (a *adminAPI) register3XUIImportRoutes(router chi.Router) {
	router.Post(adminRoutePrefix+"/imports/3x-ui/dry-run", a.dryRun3XUIImport)
	router.Post(adminRoutePrefix+"/imports/3x-ui/apply", a.apply3XUIImport)
}

func begin3XUIImport(writer http.ResponseWriter) (func(), bool) {
	select {
	case admin3XUIImportGate <- struct{}{}:
	default:
		writeAdminError(writer, http.StatusTooManyRequests, "已有 3x-ui 匯入作業正在執行")
		return nil, false
	}
	controller := http.NewResponseController(writer)
	_ = controller.SetReadDeadline(time.Now().Add(admin3XUIImportReadTimeout))
	return func() {
		_ = controller.SetReadDeadline(time.Time{})
		<-admin3XUIImportGate
	}, true
}

var admin3XUIRequiredColumns = map[string][]string{
	"users":           {"id", "username", "password"},
	"settings":        {"key", "value"},
	"inbounds":        {"id", "remark", "protocol", "settings", "stream_settings", "tag", "enable", "port"},
	"clients":         {"id", "email", "sub_id", "uuid", "password", "auth", "flow", "limit_ip", "total_gb", "expiry_time", "enable", "reset"},
	"client_inbounds": {"client_id", "inbound_id", "flow_override"},
	"client_traffics": {"email", "up", "down", "total", "expiry_time", "reset", "last_online"},
}

type admin3XUIImportReport struct {
	Ready       bool                         `json:"ready"`
	Fingerprint string                       `json:"fingerprint"`
	Source      admin3XUIImportSource        `json:"source"`
	Accounts    []admin3XUIImportAccount     `json:"accounts"`
	Inbounds    []admin3XUIImportInbound     `json:"inbounds"`
	Issues      []admin3XUIImportIssue       `json:"issues"`
	Summary     admin3XUIImportReportSummary `json:"summary"`
}

type admin3XUIImportSource struct {
	Accounts    int `json:"accounts"`
	Memberships int `json:"memberships"`
	Inbounds    int `json:"inbounds"`
}

type admin3XUIImportAccount struct {
	SourceID          int64                           `json:"source_id"`
	Name              string                          `json:"name"`
	HasSubscriptionID bool                            `json:"has_subscription_id"`
	Enabled           bool                            `json:"enabled"`
	QuotaBytes        int64                           `json:"quota_bytes"`
	ExpiresAt         int64                           `json:"expires_at"`
	MaxIPs            int                             `json:"max_ips"`
	ResetDays         int                             `json:"reset_days"`
	UploadBytes       int64                           `json:"upload_bytes"`
	DownloadBytes     int64                           `json:"download_bytes"`
	LastOnline        int64                           `json:"last_online"`
	Memberships       []admin3XUIImportAccountInbound `json:"memberships"`
}

type admin3XUIImportAccountInbound struct {
	SourceInboundID int64  `json:"source_inbound_id"`
	SourceProtocol  string `json:"source_protocol"`
	TargetTag       string `json:"target_tag,omitempty"`
	Flow            string `json:"flow,omitempty"`
	HasFlowOverride bool   `json:"has_flow_override"`
}

type admin3XUIImportInbound struct {
	SourceID  int64  `json:"source_id"`
	Tag       string `json:"tag"`
	Remark    string `json:"remark"`
	Protocol  string `json:"protocol"`
	Enabled   bool   `json:"enabled"`
	Port      int    `json:"port"`
	TargetTag string `json:"target_tag,omitempty"`
	Supported bool   `json:"supported"`
}

type admin3XUIImportIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type admin3XUIImportReportSummary struct {
	CreatableAccounts int `json:"creatable_accounts"`
	BlockedAccounts   int `json:"blocked_accounts"`
	Warnings          int `json:"warnings"`
	Errors            int `json:"errors"`
}

type admin3XUISourceClient struct {
	report   *admin3XUIImportAccount
	subID    string
	uuid     string
	password string
	auth     string
	flow     string
}

func (a *adminAPI) dryRun3XUIImport(writer http.ResponseWriter, request *http.Request) {
	finish, loaded := begin3XUIImport(writer)
	if !loaded {
		return
	}
	defer finish()
	databasePath, databaseFingerprint, mappingContent, _, err := receive3XUIImport(writer, request)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, err.Error())
		return
	}
	defer os.Remove(databasePath)
	mapping, err := parse3XUIInboundMapping(mappingContent)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, "inbound_map 格式不正確")
		return
	}
	fingerprint := fingerprint3XUIImport(databaseFingerprint, mapping)
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	report, err := a.inspect3XUIDatabase(ctx, databasePath, fingerprint, mapping)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, "無法驗證 3x-ui 資料庫")
		return
	}
	writeAdminJSON(writer, http.StatusOK, report)
}

func (a *adminAPI) apply3XUIImport(writer http.ResponseWriter, request *http.Request) {
	finish, loaded := begin3XUIImport(writer)
	if !loaded {
		return
	}
	defer finish()
	databasePath, databaseFingerprint, mappingContent, confirmation, err := receive3XUIImport(writer, request)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, err.Error())
		return
	}
	defer os.Remove(databasePath)
	mapping, err := parse3XUIInboundMapping(mappingContent)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, "inbound_map 格式不正確")
		return
	}
	fingerprint := fingerprint3XUIImport(databaseFingerprint, mapping)
	if confirmation == "" || confirmation != fingerprint {
		writeAdminError(writer, http.StatusConflict, "fingerprint 與上傳的 database 或 inbound_map 不符")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()

	a.mutation.Lock()
	defer a.unlockMutation()
	a.flushPendingTrafficLocked()
	report, err := a.inspect3XUIDatabase(ctx, databasePath, fingerprint, mapping)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, "無法驗證 3x-ui 資料庫")
		return
	}
	if !report.Ready {
		writeAdminJSON(writer, http.StatusConflict, report)
		return
	}
	sources, err := read3XUIImportSources(ctx, databasePath)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, "無法讀取 3x-ui client 資料")
		return
	}
	if err = a.commit3XUIImport(report, sources); err != nil {
		writeAdminError(writer, http.StatusInternalServerError, E.Cause(err, "3x-ui 匯入失敗").Error())
		return
	}
	writeAdminJSON(writer, http.StatusCreated, report)
}

func read3XUIImportSources(ctx context.Context, databasePath string) (map[int64]*admin3XUISourceClient, error) {
	database, err := open3XUIReadOnlyDatabase(databasePath)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.QueryContext(ctx, "SELECT id, COALESCE(sub_id, ''), COALESCE(uuid, ''), COALESCE(password, ''), COALESCE(auth, ''), COALESCE(flow, '') FROM clients ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make(map[int64]*admin3XUISourceClient)
	for rows.Next() {
		var identifier int64
		source := &admin3XUISourceClient{}
		if err = rows.Scan(&identifier, &source.subID, &source.uuid, &source.password, &source.auth, &source.flow); err != nil {
			return nil, err
		}
		sources[identifier] = source
	}
	return sources, rows.Err()
}

type admin3XUIPreparedAccount struct {
	account     *adminAccount
	subID       string
	memberships map[string]*adminUser
}

func (a *adminAPI) commit3XUIImport(report admin3XUIImportReport, sources map[int64]*admin3XUISourceClient) error {
	now := time.Now().UnixMilli()
	sourceInbounds := make(map[int64]admin3XUIImportInbound, len(report.Inbounds))
	for _, inbound := range report.Inbounds {
		sourceInbounds[inbound.SourceID] = inbound
	}
	prepared := make([]admin3XUIPreparedAccount, 0, len(report.Accounts))
	for index := range report.Accounts {
		importAccount := &report.Accounts[index]
		source := sources[importAccount.SourceID]
		if source == nil {
			return E.New("missing source client: ", importAccount.SourceID)
		}
		source.report = importAccount
		accountID, err := newAdminID()
		if err != nil {
			return err
		}
		account := &adminAccount{
			ID: accountID, Name: importAccount.Name, PolicyScope: adminAccountPolicyGlobal,
			Enabled: importAccount.Enabled, QuotaBytes: importAccount.QuotaBytes, ExpiresAt: importAccount.ExpiresAt,
			MaxIPs: importAccount.MaxIPs, ResetDays: importAccount.ResetDays,
			BaseUploadBytes: importAccount.UploadBytes, BaseDownloadBytes: importAccount.DownloadBytes,
			LastOnline: importAccount.LastOnline, Revision: nextAdminRevision(0, now), CreatedAt: now, UpdatedAt: now,
		}
		value := admin3XUIPreparedAccount{account: account, subID: source.subID, memberships: make(map[string]*adminUser, len(importAccount.Memberships))}
		for _, membership := range importAccount.Memberships {
			runtimeInbound := a.runtimes[membership.TargetTag]
			if runtimeInbound == nil || runtimeInbound.Manager == nil {
				return E.New("missing target inbound: ", membership.TargetTag)
			}
			input := make3XUIAdminUserInput(source, membership.SourceProtocol, membership.TargetTag, membership.Flow)
			enabled := sourceInbounds[membership.SourceInboundID].Enabled
			input.Enabled = &enabled
			normalized, err := normalizeAdminInput(input, runtimeInbound.Manager.service.ManagedUserSchema())
			if err != nil {
				return E.Cause(err, "invalid source credential")
			}
			userID, err := newAdminID()
			if err != nil {
				return err
			}
			value.memberships[membership.TargetTag] = &adminUser{
				ID: userID, AccountID: accountID, Inbound: membership.TargetTag, Type: runtimeInbound.Type,
				Name: importAccount.Name, UUID: normalized.UUID, Password: normalized.Password,
				Flow: normalized.Flow, AlterID: normalized.AlterID, Enabled: enabled, CreatedAt: now, UpdatedAt: now,
			}
		}
		prepared = append(prepared, value)
	}

	a.storeAccess.Lock()
	defer a.storeAccess.Unlock()
	if a.store.Accounts == nil {
		a.store.Accounts = make(map[string]*adminAccount)
	}
	if a.store.Subscriptions == nil {
		a.store.Subscriptions = make(map[string]string)
	}
	if a.store.ExternalSubscriptions == nil {
		a.store.ExternalSubscriptions = make(map[string]string)
	}
	updatedInbounds := make(map[string]*adminInboundStore)
	for _, value := range prepared {
		for _, existing := range a.store.Accounts {
			if existing != nil && strings.EqualFold(existing.Name, value.account.Name) {
				return E.New("account name already exists")
			}
		}
		if a.store.Accounts[value.account.ID] != nil {
			return E.New("generated duplicate account identifier")
		}
		if value.subID != "" {
			for _, identifier := range a.store.Subscriptions {
				if identifier == value.subID {
					return E.New("subscription identifier already exists")
				}
			}
			for _, identifier := range a.store.ExternalSubscriptions {
				if identifier == value.subID {
					return E.New("subscription identifier already exists")
				}
			}
		}
		for tag, candidate := range value.memberships {
			updated := updatedInbounds[tag]
			if updated == nil {
				updated = cloneInboundStore(a.store.Inbounds[tag])
				if updated == nil {
					return E.New("target inbound disappeared: ", tag)
				}
				updatedInbounds[tag] = updated
			}
			if err := validateUniqueUser(updated, candidate, ""); err != nil {
				return E.Cause(err, "target inbound conflict")
			}
			updated.Users = append(updated.Users, candidate)
		}
	}

	previousAccounts := cloneAdminAccounts(a.store.Accounts)
	previousSubscriptions := maps.Clone(a.store.Subscriptions)
	previousExternalSubscriptions := maps.Clone(a.store.ExternalSubscriptions)
	previousInbounds := make(map[string]*adminInboundStore, len(updatedInbounds))
	tags := make([]string, 0, len(updatedInbounds))
	for tag, updated := range updatedInbounds {
		previousInbounds[tag] = cloneInboundStore(a.store.Inbounds[tag])
		updated.Authoritative = true
		updated.Revision = nextAdminRevision(updated.Revision, now)
		a.store.Inbounds[tag] = updated
		tags = append(tags, tag)
	}
	for _, value := range prepared {
		a.store.Accounts[value.account.ID] = value.account
		if value.subID != "" {
			a.store.ExternalSubscriptions[value.account.Name] = value.subID
		}
	}
	sort.Strings(tags)
	a.storeAccess.Unlock()
	err := a.applyAndSave3XUIImport(tags, previousInbounds, previousAccounts, previousSubscriptions, previousExternalSubscriptions)
	a.storeAccess.Lock()
	return err
}

func (a *adminAPI) applyAndSave3XUIImport(tags []string, previousInbounds map[string]*adminInboundStore, previousAccounts map[string]*adminAccount, previousSubscriptions map[string]string, previousExternalSubscriptions map[string]string) error {
	restore := func(persist bool) error {
		a.storeAccess.Lock()
		maps.Copy(a.store.Inbounds, previousInbounds)
		a.store.Accounts = previousAccounts
		a.store.Subscriptions = previousSubscriptions
		a.store.ExternalSubscriptions = previousExternalSubscriptions
		a.storeAccess.Unlock()
		var restoreErr error
		for _, tag := range tags {
			restoreErr = errors.Join(restoreErr, a.applyInbound(tag, true))
		}
		if persist {
			restoreErr = errors.Join(restoreErr, a.saveStore())
		}
		return restoreErr
	}
	for _, tag := range tags {
		if err := a.applyInbound(tag, true); err != nil {
			return errors.Join(E.Cause(err, "update imported runtime"), restore(false))
		}
	}
	if err := a.saveStore(); err != nil {
		return errors.Join(E.Cause(err, "save imported store"), restore(true))
	}
	return nil
}

func receive3XUIImport(writer http.ResponseWriter, request *http.Request) (databasePath string, fingerprint string, mapping []byte, confirmation string, err error) {
	request.Body = http.MaxBytesReader(writer, request.Body, admin3XUIImportMaxDatabaseSize+admin3XUIImportMaxMappingSize+(1<<20))
	reader, err := request.MultipartReader()
	if err != nil {
		return "", "", nil, "", fmt.Errorf("請使用 multipart/form-data 上傳")
	}
	fingerprintProvided := false
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			err = fmt.Errorf("無法讀取上傳內容")
			break
		}
		switch part.FormName() {
		case "database":
			if databasePath != "" {
				err = fmt.Errorf("只能上傳一個 database 檔案")
				_ = part.Close()
				break
			}
			var output *os.File
			output, err = os.CreateTemp("", "sidera-3x-ui-import-*.db")
			if err == nil {
				databasePath = output.Name()
				hash := sha256.New()
				var size int64
				size, err = io.Copy(io.MultiWriter(output, hash), io.LimitReader(part, admin3XUIImportMaxDatabaseSize+1))
				closeErr := output.Close()
				if err == nil {
					err = closeErr
				}
				if err == nil && size > admin3XUIImportMaxDatabaseSize {
					err = fmt.Errorf("database 檔案不可超過 256 MiB")
				}
				if err == nil {
					fingerprint = "sha256:" + hex.EncodeToString(hash.Sum(nil))
				}
			}
		case "inbound_map":
			if mapping != nil {
				err = fmt.Errorf("只能提供一個 inbound_map")
				_ = part.Close()
				break
			}
			mapping, err = io.ReadAll(io.LimitReader(part, admin3XUIImportMaxMappingSize+1))
			if err == nil && len(mapping) > admin3XUIImportMaxMappingSize {
				err = fmt.Errorf("inbound_map 內容過大")
			}
		case "fingerprint":
			if fingerprintProvided {
				err = fmt.Errorf("只能提供一個 fingerprint")
				_ = part.Close()
				break
			}
			fingerprintProvided = true
			var value []byte
			value, err = io.ReadAll(io.LimitReader(part, 257))
			if err == nil && len(value) > 256 {
				err = fmt.Errorf("fingerprint 內容過大")
			}
			confirmation = string(value)
		default:
			err = fmt.Errorf("不支援的 multipart 欄位")
		}
		_ = part.Close()
		if err != nil {
			break
		}
	}
	if err != nil {
		if databasePath != "" {
			_ = os.Remove(databasePath)
		}
		return "", "", nil, "", err
	}
	if databasePath == "" {
		return "", "", nil, "", fmt.Errorf("缺少 database 檔案")
	}
	input, openErr := os.Open(databasePath)
	if openErr != nil {
		_ = os.Remove(databasePath)
		return "", "", nil, "", fmt.Errorf("database 不是 SQLite 3 檔案")
	}
	header := make([]byte, 16)
	_, readErr := io.ReadFull(input, header)
	closeErr := input.Close()
	if readErr != nil || closeErr != nil || string(header) != "SQLite format 3\x00" {
		_ = os.Remove(databasePath)
		return "", "", nil, "", fmt.Errorf("database 不是 SQLite 3 檔案")
	}
	return databasePath, fingerprint, mapping, confirmation, nil
}

func parse3XUIInboundMapping(content []byte) (map[int64]string, error) {
	if len(content) == 0 {
		return make(map[int64]string), nil
	}
	var raw map[string]string
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, err
	}
	result := make(map[int64]string, len(raw))
	for source, target := range raw {
		identifier, err := strconv.ParseInt(source, 10, 64)
		if err != nil || identifier <= 0 || source != strconv.FormatInt(identifier, 10) || target == "" || target != strings.TrimSpace(target) {
			return nil, fmt.Errorf("invalid mapping")
		}
		result[identifier] = target
	}
	return result, nil
}

func fingerprint3XUIImport(databaseFingerprint string, mapping map[int64]string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, databaseFingerprint)
	identifiers := make([]int64, 0, len(mapping))
	for identifier := range mapping {
		identifiers = append(identifiers, identifier)
	}
	slices.Sort(identifiers)
	for _, identifier := range identifiers {
		target := mapping[identifier]
		_, _ = io.WriteString(hash, "\n"+strconv.FormatInt(identifier, 10)+":"+strconv.Itoa(len(target))+":"+target)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func (a *adminAPI) inspect3XUIDatabase(ctx context.Context, databasePath string, fingerprint string, mapping map[int64]string) (admin3XUIImportReport, error) {
	report := admin3XUIImportReport{Fingerprint: fingerprint}
	database, err := open3XUIReadOnlyDatabase(databasePath)
	if err != nil {
		return report, err
	}
	defer database.Close()
	if err = database.PingContext(ctx); err != nil {
		return report, err
	}
	var integrity string
	if err = database.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return report, fmt.Errorf("integrity check failed")
	}
	tables, err := inspect3XUISchema(ctx, database, &report)
	if err != nil {
		return report, err
	}
	if report.Summary.Errors > 0 {
		finalize3XUIImportReport(&report, nil)
		return report, nil
	}

	targetTypes := make(map[string]string, len(a.runtimes))
	targetSchemas := make(map[string]adapter.ManagedUserSchema, len(a.runtimes))
	existingNames := make(map[string]map[string]bool)
	existingUUIDs := make(map[string]map[string]bool)
	existingPasswords := make(map[string]map[string]bool)
	existingAccountNames := make(map[string]bool)
	existingSubscriptionIDs := make(map[string]string)
	a.storeAccess.RLock()
	for tag, runtimeInbound := range a.runtimes {
		if runtimeInbound != nil && runtimeInbound.Manager != nil {
			targetTypes[tag] = runtimeInbound.Type
			targetSchemas[tag] = runtimeInbound.Manager.service.ManagedUserSchema()
		}
	}
	for tag, inbound := range a.store.Inbounds {
		if inbound == nil {
			continue
		}
		names := make(map[string]bool, len(inbound.Users))
		uuids := make(map[string]bool, len(inbound.Users))
		passwords := make(map[string]bool, len(inbound.Users))
		for _, user := range inbound.Users {
			if user != nil {
				names[strings.ToLower(user.Name)] = true
				if user.UUID != "" {
					uuids[user.UUID] = true
				}
				if user.Password != "" {
					passwords[user.Password] = true
				}
			}
		}
		existingNames[tag] = names
		existingUUIDs[tag] = uuids
		existingPasswords[tag] = passwords
	}
	for _, account := range a.store.Accounts {
		if account != nil {
			existingAccountNames[strings.ToLower(account.Name)] = true
		}
	}
	for name, identifier := range a.store.Subscriptions {
		existingSubscriptionIDs[identifier] = name
	}
	for name, identifier := range a.store.ExternalSubscriptions {
		existingSubscriptionIDs[identifier] = name
	}
	a.storeAccess.RUnlock()

	inbounds, inboundByID, invalidInbounds, err := inspect3XUIInbounds(ctx, database, mapping, targetTypes, &report)
	if err != nil {
		return report, err
	}
	report.Inbounds = inbounds
	blockedAccounts := make(map[int64]bool)
	clients, clientByID, err := inspect3XUIClients(ctx, database, existingAccountNames, existingSubscriptionIDs, blockedAccounts, &report)
	if err != nil {
		return report, err
	}
	report.Accounts = clients
	if err = inspect3XUIMemberships(ctx, database, mapping, targetTypes, targetSchemas, existingNames, existingUUIDs, existingPasswords, inboundByID, invalidInbounds, clientByID, blockedAccounts, &report); err != nil {
		return report, err
	}
	if tables["client_external_links"] {
		linkRows, linkErr := database.QueryContext(ctx, "SELECT DISTINCT client_id FROM client_external_links ORDER BY client_id")
		if linkErr != nil {
			return report, linkErr
		}
		for linkRows.Next() {
			var clientID int64
			if err = linkRows.Scan(&clientID); err != nil {
				_ = linkRows.Close()
				return report, err
			}
			if clientByID[clientID] == nil {
				report.addIssue("warning", "orphan_external_links", "client_external_links/"+strconv.FormatInt(clientID, 10), "外部連結沒有對應的 3x-ui client")
				continue
			}
			report.addIssue("error", "unsupported_external_links", "clients/"+strconv.FormatInt(clientID, 10), "3x-ui 外部連結尚無可無損匯入的 Sidera 模型")
			blockedAccounts[clientID] = true
		}
		if err = linkRows.Close(); err != nil {
			return report, err
		}
	}
	for sourceID := range mapping {
		if _, found := inboundByID[sourceID]; !found {
			report.addIssue("warning", "unused_mapping", "inbound_map/"+strconv.FormatInt(sourceID, 10), "映射的 3x-ui inbound 不存在")
		}
	}
	finalize3XUIImportReport(&report, blockedAccounts)
	return report, nil
}

func open3XUIReadOnlyDatabase(databasePath string) (*sql.DB, error) {
	databaseURIPath := filepath.ToSlash(databasePath)
	if filepath.VolumeName(databasePath) != "" && !strings.HasPrefix(databaseURIPath, "/") {
		databaseURIPath = "/" + databaseURIPath
	}
	databaseURL := &url.URL{Scheme: "file", Path: databaseURIPath}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	return database, nil
}

func inspect3XUISchema(ctx context.Context, database *sql.DB, report *admin3XUIImportReport) (map[string]bool, error) {
	rows, err := database.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, err
	}
	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tables[name] = true
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	tableNames := make([]string, 0, len(admin3XUIRequiredColumns))
	for table := range admin3XUIRequiredColumns {
		tableNames = append(tableNames, table)
	}
	sort.Strings(tableNames)
	for _, table := range tableNames {
		if !tables[table] {
			report.addIssue("error", "missing_table", "schema/"+table, "缺少 3x-ui 3.5.0 必要資料表")
			continue
		}
		columns, err := inspect3XUITableColumns(ctx, database, table)
		if err != nil {
			return nil, err
		}
		for _, column := range admin3XUIRequiredColumns[table] {
			if !columns[column] {
				report.addIssue("error", "missing_column", "schema/"+table+"/"+column, "缺少 3x-ui 3.5.0 必要欄位")
			}
		}
	}
	return tables, nil
}

func inspect3XUITableColumns(ctx context.Context, database *sql.DB, table string) (map[string]bool, error) {
	rows, err := database.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var identifier int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err = rows.Scan(&identifier, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func inspect3XUIInbounds(ctx context.Context, database *sql.DB, mapping map[int64]string, targetTypes map[string]string, report *admin3XUIImportReport) ([]admin3XUIImportInbound, map[int64]admin3XUIImportInbound, map[int64]bool, error) {
	rows, err := database.QueryContext(ctx, "SELECT id, COALESCE(tag, ''), COALESCE(remark, ''), lower(COALESCE(protocol, '')), COALESCE(settings, ''), COALESCE(stream_settings, ''), COALESCE(enable, 0), COALESCE(port, 0) FROM inbounds ORDER BY id")
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var inbounds []admin3XUIImportInbound
	byID := make(map[int64]admin3XUIImportInbound)
	invalid := make(map[int64]bool)
	for rows.Next() {
		var inbound admin3XUIImportInbound
		var settings, streamSettings string
		var enabled int
		if err = rows.Scan(&inbound.SourceID, &inbound.Tag, &inbound.Remark, &inbound.Protocol, &settings, &streamSettings, &enabled, &inbound.Port); err != nil {
			return nil, nil, nil, err
		}
		inbound.Protocol = normalize3XUIInboundProtocol(inbound.Protocol, settings, streamSettings)
		inbound.Enabled = enabled != 0
		if _, found := byID[inbound.SourceID]; found {
			report.addIssue("error", "duplicate_inbound_id", "inbounds/"+strconv.FormatInt(inbound.SourceID, 10), "3x-ui inbound ID 重複")
			invalid[inbound.SourceID] = true
		}
		inbound.TargetTag = mapping[inbound.SourceID]
		targetType, targetFound := targetTypes[inbound.TargetTag]
		inbound.Supported = targetFound && compatible3XUIProtocol(inbound.Protocol, targetType)
		path := "inbounds/" + strconv.FormatInt(inbound.SourceID, 10)
		switch {
		case inbound.TargetTag == "":
			report.addIssue("error", "unmapped_inbound", path, "3x-ui inbound 尚未映射到 Sidera server tag")
			invalid[inbound.SourceID] = true
		case !targetFound:
			report.addIssue("error", "missing_target_inbound", path, "映射的 Sidera inbound 不存在或不支援動態用戶")
			invalid[inbound.SourceID] = true
		case !inbound.Supported:
			report.addIssue("error", "unsupported_protocol_mapping", path, "來源與目標 inbound 協定不相容")
			invalid[inbound.SourceID] = true
		}
		inbounds = append(inbounds, inbound)
		byID[inbound.SourceID] = inbound
	}
	report.Source.Inbounds = len(inbounds)
	return inbounds, byID, invalid, rows.Err()
}

func normalize3XUIInboundProtocol(protocol string, settings string, streamSettings string) string {
	if protocol == C.TypeHysteria2 {
		return protocol
	}
	if protocol != C.TypeHysteria {
		return protocol
	}
	if hysteriaJSONVersion(settings) == 2 {
		return C.TypeHysteria2
	}
	var stream struct {
		Version          int `json:"version"`
		HysteriaSettings struct {
			Version int `json:"version"`
		} `json:"hysteriaSettings"`
	}
	if json.Unmarshal([]byte(streamSettings), &stream) == nil && (stream.Version == 2 || stream.HysteriaSettings.Version == 2) {
		return C.TypeHysteria2
	}
	return protocol
}

func hysteriaJSONVersion(raw string) int {
	if raw == "" {
		return 0
	}
	var options struct {
		Version int `json:"version"`
	}
	if json.Unmarshal([]byte(raw), &options) != nil {
		return 0
	}
	return options.Version
}

func inspect3XUIClients(ctx context.Context, database *sql.DB, existingAccountNames map[string]bool, existingSubscriptionIDs map[string]string, blocked map[int64]bool, report *admin3XUIImportReport) ([]admin3XUIImportAccount, map[int64]*admin3XUISourceClient, error) {
	rows, err := database.QueryContext(ctx, "SELECT id, COALESCE(email, ''), COALESCE(sub_id, ''), COALESCE(uuid, ''), COALESCE(password, ''), COALESCE(auth, ''), COALESCE(flow, ''), COALESCE(limit_ip, 0), COALESCE(total_gb, 0), COALESCE(expiry_time, 0), COALESCE(enable, 0), COALESCE(reset, 0) FROM clients ORDER BY id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var accounts []admin3XUIImportAccount
	byID := make(map[int64]*admin3XUISourceClient)
	bySubID := make(map[string][]int64)
	byName := make(map[string]int64)
	for rows.Next() {
		account := admin3XUIImportAccount{}
		source := &admin3XUISourceClient{report: &account}
		var enabled int
		if err = rows.Scan(&account.SourceID, &account.Name, &source.subID, &source.uuid, &source.password, &source.auth, &source.flow, &account.MaxIPs, &account.QuotaBytes, &account.ExpiresAt, &enabled, &account.ResetDays); err != nil {
			return nil, nil, err
		}
		if _, found := byID[account.SourceID]; found {
			report.addIssue("error", "duplicate_client_id", "clients/"+strconv.FormatInt(account.SourceID, 10), "3x-ui client ID 重複")
			blocked[account.SourceID] = true
		}
		account.Enabled = enabled != 0
		account.HasSubscriptionID = source.subID != ""
		if account.Name == "" {
			report.addIssue("error", "empty_client_name", "clients/"+strconv.FormatInt(account.SourceID, 10), "3x-ui client email 不可為空")
			blocked[account.SourceID] = true
		} else if len(account.Name) > 128 {
			report.addIssue("error", "client_name_too_long", "clients/"+strconv.FormatInt(account.SourceID, 10), "3x-ui client email 超過 Sidera 支援的長度")
			blocked[account.SourceID] = true
		}
		nameKey := strings.ToLower(account.Name)
		if existingAccountNames[nameKey] {
			report.addIssue("error", "existing_account", "clients/"+strconv.FormatInt(account.SourceID, 10), "Sidera 已有相同名稱的帳戶")
			blocked[account.SourceID] = true
		}
		if previous, found := byName[nameKey]; account.Name != "" && found {
			report.addIssue("error", "duplicate_client_name", "clients/"+strconv.FormatInt(account.SourceID, 10), "3x-ui client email 與另一筆 client 重複；來源 ID "+strconv.FormatInt(previous, 10))
			blocked[previous] = true
			blocked[account.SourceID] = true
		} else if account.Name != "" {
			byName[nameKey] = account.SourceID
		}
		if source.subID != "" {
			bySubID[source.subID] = append(bySubID[source.subID], account.SourceID)
			if !validExternalSubscriptionID(source.subID) {
				report.addIssue("error", "invalid_subscription_id", "clients/"+strconv.FormatInt(account.SourceID, 10), "3x-ui 訂閱 ID 無法作為 Sidera 相容路徑")
				blocked[account.SourceID] = true
			} else if owner, found := existingSubscriptionIDs[source.subID]; found {
				report.addIssue("error", "existing_subscription_id", "clients/"+strconv.FormatInt(account.SourceID, 10), "3x-ui 訂閱 ID 已由 Sidera 帳戶 "+owner+" 使用")
				blocked[account.SourceID] = true
			}
		}
		if account.MaxIPs < 0 || account.QuotaBytes < 0 || account.ExpiresAt < 0 || account.ResetDays < 0 || int64(account.ResetDays) > math.MaxInt64/adminDayMilliseconds {
			report.addIssue("error", "invalid_client_limits", "clients/"+strconv.FormatInt(account.SourceID, 10), "3x-ui client 含有 Sidera 不接受的負數限制")
			blocked[account.SourceID] = true
		}
		accounts = append(accounts, account)
		byID[account.SourceID] = source
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	trafficRows, err := database.QueryContext(ctx, "SELECT COALESCE(email, ''), COALESCE(up, 0), COALESCE(down, 0), COALESCE(last_online, 0) FROM client_traffics")
	if err != nil {
		return nil, nil, err
	}
	accountByName := make(map[string]*admin3XUIImportAccount, len(accounts))
	for index := range accounts {
		accountByName[accounts[index].Name] = &accounts[index]
	}
	trafficSeen := make(map[string]bool, len(accounts))
	for trafficRows.Next() {
		var name string
		var upload, download, lastOnline int64
		if err = trafficRows.Scan(&name, &upload, &download, &lastOnline); err != nil {
			_ = trafficRows.Close()
			return nil, nil, err
		}
		if trafficSeen[name] {
			report.addIssue("error", "duplicate_client_traffic", "client_traffics/"+name, "同一 client 有多筆全域流量紀錄")
			if account := accountByName[name]; account != nil {
				blocked[account.SourceID] = true
			}
			continue
		}
		trafficSeen[name] = true
		if account := accountByName[name]; account != nil {
			if upload < 0 || download < 0 || lastOnline < 0 {
				report.addIssue("error", "invalid_client_traffic", "client_traffics/"+name, "3x-ui client 含有 Sidera 不接受的負數流量或時間")
				blocked[account.SourceID] = true
				continue
			}
			account.UploadBytes = upload
			account.DownloadBytes = download
			account.LastOnline = lastOnline
		} else {
			report.addIssue("warning", "orphan_traffic", "client_traffics/"+name, "流量紀錄沒有對應的 3x-ui client")
		}
	}
	if err = trafficRows.Close(); err != nil {
		return nil, nil, err
	}
	for _, identifiers := range bySubID {
		if len(identifiers) < 2 {
			continue
		}
		for _, identifier := range identifiers {
			report.addIssue("error", "duplicate_subscription_id", "clients/"+strconv.FormatInt(identifier, 10), "3x-ui 訂閱 ID 與其他 client 重複")
			blocked[identifier] = true
		}
	}
	for index := range accounts {
		byID[accounts[index].SourceID].report = &accounts[index]
	}
	report.Source.Accounts = len(accounts)
	return accounts, byID, nil
}

func inspect3XUIMemberships(ctx context.Context, database *sql.DB, mapping map[int64]string, targetTypes map[string]string, targetSchemas map[string]adapter.ManagedUserSchema, existingNames map[string]map[string]bool, existingUUIDs map[string]map[string]bool, existingPasswords map[string]map[string]bool, inboundByID map[int64]admin3XUIImportInbound, invalidInbounds map[int64]bool, clients map[int64]*admin3XUISourceClient, blocked map[int64]bool, report *admin3XUIImportReport) error {
	rows, err := database.QueryContext(ctx, "SELECT client_id, inbound_id, COALESCE(flow_override, '') FROM client_inbounds ORDER BY client_id, inbound_id")
	if err != nil {
		return err
	}
	defer rows.Close()
	seenTarget := make(map[int64]map[string]bool)
	importedUUIDs := make(map[string]map[string]int64)
	importedPasswords := make(map[string]map[string]int64)
	for rows.Next() {
		var clientID, inboundID int64
		var flow string
		if err = rows.Scan(&clientID, &inboundID, &flow); err != nil {
			return err
		}
		report.Source.Memberships++
		path := "client_inbounds/" + strconv.FormatInt(clientID, 10) + "/" + strconv.FormatInt(inboundID, 10)
		client := clients[clientID]
		inbound, inboundFound := inboundByID[inboundID]
		if client == nil {
			report.addIssue("error", "orphan_membership_client", path, "membership 沒有對應的 3x-ui client")
			continue
		}
		if !inboundFound {
			report.addIssue("error", "orphan_membership_inbound", path, "membership 沒有對應的 3x-ui inbound")
			blocked[clientID] = true
			continue
		}
		targetTag := mapping[inboundID]
		client.report.Memberships = append(client.report.Memberships, admin3XUIImportAccountInbound{
			SourceInboundID: inboundID,
			SourceProtocol:  inbound.Protocol,
			TargetTag:       targetTag,
			Flow:            flow,
			HasFlowOverride: flow != "",
		})
		if invalidInbounds[inboundID] {
			blocked[clientID] = true
			continue
		}
		if seenTarget[clientID] == nil {
			seenTarget[clientID] = make(map[string]bool)
		}
		if seenTarget[clientID][targetTag] {
			report.addIssue("error", "duplicate_target_membership", path, "同一 client 的多個來源 membership 映射到同一 Sidera inbound")
			blocked[clientID] = true
			continue
		}
		seenTarget[clientID][targetTag] = true
		if existingNames[targetTag][strings.ToLower(client.report.Name)] {
			report.addIssue("error", "existing_target_client", path, "目標 Sidera inbound 已有相同名稱的用戶")
			blocked[clientID] = true
		}
		if !has3XUICredential(client, inbound.Protocol) {
			report.addIssue("error", "missing_credential", path, "來源 client 缺少該協定所需的憑證")
			blocked[clientID] = true
		} else if normalized, normalizeErr := normalizeAdminInput(make3XUIAdminUserInput(client, inbound.Protocol, targetTag, flow), targetSchemas[targetTag]); normalizeErr != nil {
			report.addIssue("error", "invalid_credential", path, "來源 client 憑證不符合目標 inbound 要求")
			blocked[clientID] = true
		} else {
			credentialConflict := normalized.UUID != "" && existingUUIDs[targetTag][normalized.UUID] || normalized.Password != "" && existingPasswords[targetTag][normalized.Password]
			if previous, found := importedUUIDs[targetTag][normalized.UUID]; normalized.UUID != "" && found {
				credentialConflict = true
				blocked[previous] = true
			}
			if previous, found := importedPasswords[targetTag][normalized.Password]; normalized.Password != "" && found {
				credentialConflict = true
				blocked[previous] = true
			}
			if credentialConflict {
				report.addIssue("error", "duplicate_target_credential", path, "來源 client 憑證已由目標 inbound 的其他用戶使用")
				blocked[clientID] = true
			}
			if importedUUIDs[targetTag] == nil {
				importedUUIDs[targetTag] = make(map[string]int64)
				importedPasswords[targetTag] = make(map[string]int64)
			}
			if normalized.UUID != "" {
				importedUUIDs[targetTag][normalized.UUID] = clientID
			}
			if normalized.Password != "" {
				importedPasswords[targetTag][normalized.Password] = clientID
			}
		}
		if !compatible3XUIProtocol(inbound.Protocol, targetTypes[targetTag]) {
			blocked[clientID] = true
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for identifier, client := range clients {
		if len(client.report.Memberships) == 0 {
			report.addIssue("error", "orphan_client", "clients/"+strconv.FormatInt(identifier, 10), "3x-ui client 未附加到任何 inbound")
			blocked[identifier] = true
		}
	}
	return nil
}

func make3XUIAdminUserInput(client *admin3XUISourceClient, protocol string, targetTag string, flow string) adminUserInput {
	enabled := true
	input := adminUserInput{Inbound: targetTag, Name: client.report.Name, Flow: flow, Enabled: &enabled}
	switch protocol {
	case C.TypeVLESS, C.TypeVMess:
		input.UUID = client.uuid
	case C.TypeHysteria, C.TypeHysteria2:
		input.Password = client.auth
	default:
		input.Password = client.password
	}
	return input
}

func compatible3XUIProtocol(source string, target string) bool {
	switch source {
	case C.TypeVLESS, C.TypeVMess, C.TypeTrojan, C.TypeShadowsocks, C.TypeSOCKS, C.TypeHTTP, C.TypeMixed, C.TypeHysteria, C.TypeHysteria2:
		return source == target
	default:
		return false
	}
}

func has3XUICredential(client *admin3XUISourceClient, protocol string) bool {
	switch protocol {
	case C.TypeVLESS, C.TypeVMess:
		return client.uuid != ""
	case C.TypeHysteria, C.TypeHysteria2:
		return client.auth != ""
	case C.TypeTrojan, C.TypeShadowsocks, C.TypeSOCKS, C.TypeHTTP, C.TypeMixed:
		return client.password != ""
	default:
		return false
	}
}

func (r *admin3XUIImportReport) addIssue(severity string, code string, path string, message string) {
	r.Issues = append(r.Issues, admin3XUIImportIssue{Severity: severity, Code: code, Path: path, Message: message})
	if severity == "error" {
		r.Summary.Errors++
	} else {
		r.Summary.Warnings++
	}
}

func finalize3XUIImportReport(report *admin3XUIImportReport, blocked map[int64]bool) {
	blockedAccounts := 0
	for index := range report.Accounts {
		if blocked[report.Accounts[index].SourceID] {
			blockedAccounts++
		}
		sort.Slice(report.Accounts[index].Memberships, func(left, right int) bool {
			return report.Accounts[index].Memberships[left].SourceInboundID < report.Accounts[index].Memberships[right].SourceInboundID
		})
	}
	sort.Slice(report.Accounts, func(left, right int) bool { return report.Accounts[left].SourceID < report.Accounts[right].SourceID })
	sort.Slice(report.Inbounds, func(left, right int) bool { return report.Inbounds[left].SourceID < report.Inbounds[right].SourceID })
	severityRank := func(value string) int {
		if value == "error" {
			return 0
		}
		return 1
	}
	sort.Slice(report.Issues, func(left, right int) bool {
		leftIssue, rightIssue := report.Issues[left], report.Issues[right]
		if severityRank(leftIssue.Severity) != severityRank(rightIssue.Severity) {
			return severityRank(leftIssue.Severity) < severityRank(rightIssue.Severity)
		}
		if leftIssue.Code != rightIssue.Code {
			return leftIssue.Code < rightIssue.Code
		}
		return leftIssue.Path < rightIssue.Path
	})
	report.Summary.BlockedAccounts = blockedAccounts
	report.Summary.CreatableAccounts = max(len(report.Accounts)-blockedAccounts, 0)
	report.Ready = report.Summary.Errors == 0 && report.Summary.BlockedAccounts == 0
}
