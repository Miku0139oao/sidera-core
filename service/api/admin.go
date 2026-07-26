package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/trafficcontrol"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/daemon"
	"github.com/Miku0139oao/sidera-core/log"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"

	"github.com/go-chi/chi/v5"
	"github.com/gofrs/uuid/v5"
)

const (
	adminRoutePrefix     = "/api/admin"
	adminStoreVersion    = 2
	defaultAdminDataPath = "sidera-dashboard.json"
)

type adminAPI struct {
	ctx       context.Context
	logger    log.ContextLogger
	secret    string
	dataPath  string
	router    http.Handler
	startedAt time.Time

	traffic  *trafficcontrol.Manager
	runtimes map[string]*adminInboundRuntime
	inbounds []adminInboundRuntime

	storeAccess       sync.RWMutex
	store             adminStore
	mutation          sync.Mutex
	saveAccess        sync.Mutex
	trafficAccess     sync.Mutex
	trafficBaselines  map[uuid.UUID]adminTrafficBaseline
	runCtx            context.Context
	cancel            context.CancelFunc
	workers           sync.WaitGroup
	removeTrafficHook func()
	dirty             atomic.Bool
}

type adminInboundRuntime struct {
	Tag     string
	Type    string
	Kind    string
	Manager *adminManagedRuntime
}

type adminManagedRuntime struct {
	service       adapter.ManagedUserService
	applyAccess   sync.Mutex
	lastSignature string
}

type adminStore struct {
	Version  int                           `json:"version"`
	Inbounds map[string]*adminInboundStore `json:"inbounds"`
	Servers  map[string]*adminServerStore  `json:"servers,omitempty"`
}

type adminServerStore struct {
	Kind            string               `json:"kind"`
	Type            string               `json:"type"`
	Config          json.RawMessage      `json:"config"`
	Advertise       adminServerAdvertise `json:"advertise,omitempty"`
	Revision        int64                `json:"revision"`
	AppliedRevision int64                `json:"applied_revision,omitempty"`
	Deleted         bool                 `json:"deleted,omitempty"`
	CreatedAt       int64                `json:"created_at"`
	UpdatedAt       int64                `json:"updated_at"`
}

type adminServerAdvertise struct {
	Server        string `json:"server,omitempty"`
	ServerPort    uint16 `json:"server_port,omitempty"`
	TLSServerName string `json:"tls_server_name,omitempty"`
	Insecure      bool   `json:"insecure,omitempty"`
}

type adminInboundStore struct {
	Type          string       `json:"type"`
	Authoritative bool         `json:"authoritative"`
	BlockUUID     string       `json:"block_uuid"`
	BlockPassword string       `json:"block_password"`
	Users         []*adminUser `json:"users"`
}

type adminUser struct {
	ID            string `json:"id"`
	Inbound       string `json:"inbound"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	UUID          string `json:"uuid,omitempty"`
	Password      string `json:"password,omitempty"`
	Flow          string `json:"flow,omitempty"`
	AlterID       int    `json:"alter_id,omitempty"`
	Enabled       bool   `json:"enabled"`
	QuotaBytes    int64  `json:"quota_bytes"`
	ExpiresAt     int64  `json:"expires_at"`
	UploadBytes   int64  `json:"upload_bytes"`
	DownloadBytes int64  `json:"download_bytes"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

type adminUserView struct {
	adminUser
	ActiveConnections int `json:"active_connections"`
}

type adminUserInput struct {
	Inbound    string `json:"inbound"`
	Name       string `json:"name"`
	UUID       string `json:"uuid"`
	Password   string `json:"password"`
	Flow       string `json:"flow"`
	AlterID    int    `json:"alter_id"`
	Enabled    *bool  `json:"enabled"`
	QuotaBytes int64  `json:"quota_bytes"`
	ExpiresAt  int64  `json:"expires_at"`
}

type adminInboundSummary struct {
	Tag              string `json:"tag"`
	Type             string `json:"type"`
	Managed          bool   `json:"managed"`
	Credential       string `json:"credential,omitempty"`
	PasswordEncoding string `json:"password_encoding,omitempty"`
	PasswordBytes    int    `json:"password_bytes,omitempty"`
	Flow             bool   `json:"flow,omitempty"`
	AlterID          bool   `json:"alter_id,omitempty"`
	Traffic          bool   `json:"traffic"`
	UserCount        int    `json:"user_count"`
	EnabledUserCount int    `json:"enabled_user_count"`
}

type adminUsage struct {
	Upload      int64
	Download    int64
	Connections int
}

type adminTrafficBaseline struct {
	Upload   int64
	Download int64
}

func newAdminAPI(ctx context.Context, logger log.ContextLogger, secret string, dataPath string) (*adminAPI, error) {
	if dataPath == "" {
		dataPath = defaultAdminDataPath
	}
	runCtx, cancel := context.WithCancel(ctx)
	a := &adminAPI{
		ctx:              ctx,
		runCtx:           runCtx,
		cancel:           cancel,
		logger:           logger,
		secret:           secret,
		dataPath:         filemanager.BasePath(ctx, os.ExpandEnv(dataPath)),
		traffic:          service.PtrFromContext[trafficcontrol.Manager](ctx),
		runtimes:         make(map[string]*adminInboundRuntime),
		trafficBaselines: make(map[uuid.UUID]adminTrafficBaseline),
		store: adminStore{
			Version:  adminStoreVersion,
			Inbounds: make(map[string]*adminInboundStore),
			Servers:  make(map[string]*adminServerStore),
		},
	}
	a.discoverServices(ctx)
	if err := a.loadStore(); err != nil {
		cancel()
		return nil, err
	}
	if err := a.synchronizeStore(); err != nil {
		cancel()
		return nil, err
	}
	if err := a.applyAll(true); err != nil {
		cancel()
		return nil, E.Cause(err, "apply dashboard users")
	}
	if err := a.saveStore(); err != nil {
		cancel()
		return nil, E.Cause(err, "save dashboard users")
	}
	a.router = a.buildRouter()
	return a, nil
}

func (a *adminAPI) discoverServices(ctx context.Context) {
	inboundManager := service.FromContext[adapter.InboundManager](ctx)
	if inboundManager != nil {
		for _, inbound := range inboundManager.Inbounds() {
			a.addRuntime(inbound.Tag(), inbound.Type(), adminServerKindInbound, inbound)
		}
	}
	endpointManager := service.FromContext[adapter.EndpointManager](ctx)
	if endpointManager != nil {
		for _, endpoint := range endpointManager.Endpoints() {
			if _, exists := a.runtimes[endpoint.Tag()]; exists {
				continue
			}
			a.addRuntime(endpoint.Tag(), endpoint.Type(), adminServerKindEndpoint, endpoint)
		}
	}
	sort.Slice(a.inbounds, func(i, j int) bool {
		return a.inbounds[i].Tag < a.inbounds[j].Tag
	})
}

func (a *adminAPI) addRuntime(tag string, inboundType string, kind string, candidate any) {
	runtimeInbound := adminInboundRuntime{Tag: tag, Type: inboundType, Kind: kind}
	if managed, loaded := candidate.(adapter.ManagedUserService); loaded && managed.ManagedUserSchema().Credential != "" {
		runtimeInbound.Manager = &adminManagedRuntime{service: managed}
	}
	a.runtimes[tag] = &runtimeInbound
	a.inbounds = append(a.inbounds, runtimeInbound)
}

func (a *adminAPI) loadStore() error {
	content, err := filemanager.ReadFile(a.ctx, a.dataPath)
	if errors.Is(err, os.ErrNotExist) {
		content, err = filemanager.ReadFile(a.ctx, a.dataPath+".bak")
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if err != nil {
		return E.Cause(err, "read dashboard data")
	}
	var stored adminStore
	if err = json.Unmarshal(content, &stored); err != nil {
		return E.Cause(err, "decode dashboard data")
	}
	if stored.Version == 1 {
		stored.Version = adminStoreVersion
	} else if stored.Version != adminStoreVersion {
		return E.New("unsupported dashboard data version: ", stored.Version)
	}
	if stored.Inbounds == nil {
		stored.Inbounds = make(map[string]*adminInboundStore)
	}
	if stored.Servers == nil {
		stored.Servers = make(map[string]*adminServerStore)
	}
	for tag, record := range stored.Inbounds {
		if record == nil {
			return E.New("invalid null dashboard inbound: ", tag)
		}
		for index, user := range record.Users {
			if user == nil {
				return E.New("invalid null dashboard user in ", tag, " at index ", index)
			}
		}
	}
	a.store = stored
	return nil
}

func (a *adminAPI) synchronizeStore() error {
	now := time.Now().UnixMilli()
	for index := range a.inbounds {
		runtimeInbound := &a.inbounds[index]
		if runtimeInbound.Manager == nil {
			continue
		}
		profile := a.store.Servers[runtimeInbound.Tag]
		dashboardOwned := profile != nil && !profile.Deleted
		record, exists := a.store.Inbounds[runtimeInbound.Tag]
		if !exists || record.Type != runtimeInbound.Type {
			record = &adminInboundStore{Type: runtimeInbound.Type}
			currentUsers := runtimeInbound.Manager.service.ManagedUsers()
			record.Authoritative = dashboardOwned && len(currentUsers) > 0
			for _, currentUser := range currentUsers {
				id, err := newAdminID()
				if err != nil {
					return err
				}
				record.Users = append(record.Users, &adminUser{
					ID:        id,
					Inbound:   runtimeInbound.Tag,
					Type:      runtimeInbound.Type,
					Name:      currentUser.Name,
					UUID:      currentUser.UUID,
					Password:  currentUser.Password,
					Flow:      currentUser.Flow,
					AlterID:   currentUser.AlterID,
					Enabled:   true,
					CreatedAt: now,
					UpdatedAt: now,
				})
			}
			a.store.Inbounds[runtimeInbound.Tag] = record
		} else if !record.Authoritative {
			currentUsers := runtimeInbound.Manager.service.ManagedUsers()
			if len(currentUsers) > 0 {
				record.Authoritative = dashboardOwned
				record.Users = nil
				for _, currentUser := range currentUsers {
					id, err := newAdminID()
					if err != nil {
						return err
					}
					record.Users = append(record.Users, &adminUser{
						ID: id, Inbound: runtimeInbound.Tag, Type: runtimeInbound.Type,
						Name: currentUser.Name, UUID: currentUser.UUID, Password: currentUser.Password,
						Flow: currentUser.Flow, AlterID: currentUser.AlterID, Enabled: true,
						CreatedAt: now, UpdatedAt: now,
					})
				}
			}
		}
		if err := ensureBlockCredentials(record, runtimeInbound.Manager.service.ManagedUserSchema()); err != nil {
			return err
		}
		for _, user := range record.Users {
			user.Inbound = runtimeInbound.Tag
			user.Type = runtimeInbound.Type
			if user.ID == "" {
				id, err := newAdminID()
				if err != nil {
					return err
				}
				user.ID = id
			}
		}
	}
	for tag, profile := range a.store.Servers {
		if profile == nil {
			return E.New("invalid null dashboard server: ", tag)
		}
	}
	return nil
}

func ensureBlockCredentials(record *adminInboundStore, schema adapter.ManagedUserSchema) error {
	if record.BlockUUID == "" {
		id, err := uuid.NewV4()
		if err != nil {
			return err
		}
		record.BlockUUID = id.String()
	}
	if schema.PasswordEncoding == adapter.ManagedUserPasswordBase64 && schema.PasswordBytes > 0 {
		decoded, err := base64.StdEncoding.DecodeString(record.BlockPassword)
		if err != nil || len(decoded) != schema.PasswordBytes {
			record.BlockPassword, err = randomAdminKey(schema.PasswordBytes)
			if err != nil {
				return err
			}
		}
	} else if record.BlockPassword == "" {
		buffer := make([]byte, 32)
		if _, err := rand.Read(buffer); err != nil {
			return err
		}
		record.BlockPassword = hex.EncodeToString(buffer)
	}
	return nil
}

func newAdminID() (string, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func (a *adminAPI) start() error {
	if err := a.markServerProfilesApplied(); err != nil {
		return err
	}
	a.startedAt = time.Now()
	if a.traffic != nil {
		a.removeTrafficHook = a.traffic.AddCloseHook(a.recordTraffic)
	}
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		a.maintenanceLoop()
	}()
	return nil
}

func (a *adminAPI) markServerProfilesApplied() error {
	changed := false
	a.storeAccess.Lock()
	for tag, profile := range a.store.Servers {
		runtimeInbound := a.runtimes[tag]
		if profile.Deleted {
			if runtimeInbound == nil {
				delete(a.store.Servers, tag)
				delete(a.store.Inbounds, tag)
				changed = true
			}
			continue
		}
		if runtimeInbound != nil && runtimeInbound.Type == profile.Type && runtimeInbound.Kind == profile.Kind && profile.AppliedRevision != profile.Revision {
			profile.AppliedRevision = profile.Revision
			changed = true
		}
	}
	a.storeAccess.Unlock()
	if changed {
		return a.saveStore()
	}
	return nil
}

func (a *adminAPI) close() {
	if a.removeTrafficHook != nil {
		a.removeTrafficHook()
		a.removeTrafficHook = nil
	}
	a.cancel()
	a.workers.Wait()
	a.snapshotActiveTraffic()
	if err := a.saveStore(); err != nil {
		a.logger.Error("dashboard: save data: ", err)
	}
}

func (a *adminAPI) recordTraffic(metadata *trafficcontrol.TrackerMetadata) {
	userName := metadata.Metadata.User
	if userName == "" {
		return
	}
	a.mutation.Lock()
	defer a.mutation.Unlock()
	a.trafficAccess.Lock()
	baseline := a.trafficBaselines[metadata.ID]
	delete(a.trafficBaselines, metadata.ID)
	upload := max(metadata.Upload.Load()-baseline.Upload, 0)
	download := max(metadata.Download.Load()-baseline.Download, 0)
	a.storeAccess.Lock()
	record := a.store.Inbounds[metadata.Metadata.Inbound]
	if record != nil {
		for _, user := range record.Users {
			if user.Name == userName {
				user.UploadBytes += upload
				user.DownloadBytes += download
				user.UpdatedAt = time.Now().UnixMilli()
				a.dirty.Store(true)
				break
			}
		}
	}
	a.storeAccess.Unlock()
	a.trafficAccess.Unlock()
}

func (a *adminAPI) maintenanceLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.runCtx.Done():
			return
		case <-ticker.C:
		}
		if err := a.reconcile(); err != nil {
			a.logger.Error("dashboard: reconcile users: ", err)
		}
		if a.dirty.Swap(false) {
			if err := a.saveStore(); err != nil {
				a.logger.Error("dashboard: save data: ", err)
				a.dirty.Store(true)
			}
		}
	}
}

func (a *adminAPI) reconcile() error {
	if err := a.applyAll(false); err != nil {
		return err
	}
	if a.traffic == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	usage := a.activeUsage()
	for _, metadata := range a.traffic.Connections() {
		if metadata.Metadata.User == "" {
			continue
		}
		key := adminUserKey(metadata.Metadata.Inbound, metadata.Metadata.User)
		a.storeAccess.RLock()
		allowed := a.userAllowedLocked(metadata.Metadata.Inbound, metadata.Metadata.User, now, usage[key])
		a.storeAccess.RUnlock()
		if !allowed {
			if connection := a.traffic.Connection(metadata.ID); connection != nil {
				_ = connection.Close()
			}
		}
	}
	return nil
}

func (a *adminAPI) userAllowedLocked(inboundTag string, userName string, now int64, usage adminUsage) bool {
	record := a.store.Inbounds[inboundTag]
	if record == nil || !record.Authoritative {
		return true
	}
	for _, user := range record.Users {
		if user.Name == userName {
			return adminUserEnabled(user, now, usage)
		}
	}
	return false
}

func adminUserEnabled(user *adminUser, now int64, active adminUsage) bool {
	if !user.Enabled || user.ExpiresAt > 0 && user.ExpiresAt <= now {
		return false
	}
	used := user.UploadBytes + user.DownloadBytes + active.Upload + active.Download
	return user.QuotaBytes <= 0 || used < user.QuotaBytes
}

func (a *adminAPI) applyAll(force bool) error {
	for tag, runtimeInbound := range a.runtimes {
		if runtimeInbound.Manager == nil {
			continue
		}
		if err := a.applyInbound(tag, force); err != nil {
			return E.Cause(err, "update inbound ", tag)
		}
	}
	return nil
}

func (a *adminAPI) applyInbound(tag string, force bool) error {
	runtimeInbound := a.runtimes[tag]
	if runtimeInbound == nil || runtimeInbound.Manager == nil {
		return os.ErrNotExist
	}
	active := a.activeUsage()
	now := time.Now().UnixMilli()
	a.storeAccess.RLock()
	record := a.store.Inbounds[tag]
	if record == nil {
		a.storeAccess.RUnlock()
		return os.ErrNotExist
	}
	users := make([]adapter.ManagedUser, 0, len(record.Users))
	for _, user := range record.Users {
		if adminUserEnabled(user, now, active[adminUserKey(tag, user.Name)]) {
			users = append(users, adapter.ManagedUser{
				Name:     user.Name,
				UUID:     user.UUID,
				Password: user.Password,
				Flow:     user.Flow,
				AlterID:  user.AlterID,
			})
		}
	}
	if len(users) == 0 && record.Authoritative {
		schema := runtimeInbound.Manager.service.ManagedUserSchema()
		blocked := adapter.ManagedUser{Name: "__sidera_blocked__"}
		switch schema.Credential {
		case adapter.ManagedUserCredentialUUID:
			blocked.UUID = record.BlockUUID
		case adapter.ManagedUserCredentialUUIDPassword:
			blocked.UUID = record.BlockUUID
			blocked.Password = record.BlockPassword
		default:
			blocked.Password = record.BlockPassword
		}
		users = append(users, blocked)
	}
	a.storeAccess.RUnlock()
	signatureBytes, _ := json.Marshal(users)
	signature := string(signatureBytes)

	manager := runtimeInbound.Manager
	manager.applyAccess.Lock()
	defer manager.applyAccess.Unlock()
	if !force && manager.lastSignature == signature {
		return nil
	}
	if err := manager.service.UpdateManagedUsers(users); err != nil {
		return err
	}
	manager.lastSignature = signature
	return nil
}

func (a *adminAPI) activeUsage() map[string]adminUsage {
	result := make(map[string]adminUsage)
	if a.traffic == nil {
		return result
	}
	a.trafficAccess.Lock()
	defer a.trafficAccess.Unlock()
	for _, metadata := range a.traffic.Connections() {
		if metadata.Metadata.User == "" {
			continue
		}
		key := adminUserKey(metadata.Metadata.Inbound, metadata.Metadata.User)
		usage := result[key]
		baseline := a.trafficBaselines[metadata.ID]
		usage.Upload += max(metadata.Upload.Load()-baseline.Upload, 0)
		usage.Download += max(metadata.Download.Load()-baseline.Download, 0)
		usage.Connections++
		result[key] = usage
	}
	return result
}

// baselineUserTrafficLocked resets active-connection accounting for a user.
// When settle is true, bytes before the new baseline are first persisted.
// trafficAccess must be held by the caller.
func (a *adminAPI) baselineUserTrafficLocked(tag string, userName string, settle bool) {
	if a.traffic == nil || userName == "" {
		return
	}
	var settledUpload, settledDownload int64
	for _, metadata := range a.traffic.Connections() {
		if metadata.Metadata.Inbound != tag || metadata.Metadata.User != userName {
			continue
		}
		baseline := a.trafficBaselines[metadata.ID]
		currentUpload := metadata.Upload.Load()
		currentDownload := metadata.Download.Load()
		if settle {
			settledUpload += max(currentUpload-baseline.Upload, 0)
			settledDownload += max(currentDownload-baseline.Download, 0)
		}
		a.trafficBaselines[metadata.ID] = adminTrafficBaseline{Upload: currentUpload, Download: currentDownload}
	}
	if !settle || settledUpload == 0 && settledDownload == 0 {
		return
	}
	a.storeAccess.Lock()
	if record := a.store.Inbounds[tag]; record != nil {
		for _, user := range record.Users {
			if user.Name == userName {
				user.UploadBytes += settledUpload
				user.DownloadBytes += settledDownload
				user.UpdatedAt = time.Now().UnixMilli()
				break
			}
		}
	}
	a.storeAccess.Unlock()
}

func (a *adminAPI) snapshotActiveTraffic() {
	if a.traffic == nil {
		return
	}
	a.mutation.Lock()
	defer a.mutation.Unlock()
	a.trafficAccess.Lock()
	defer a.trafficAccess.Unlock()
	a.storeAccess.Lock()
	defer a.storeAccess.Unlock()
	for _, metadata := range a.traffic.Connections() {
		userName := metadata.Metadata.User
		if userName == "" {
			continue
		}
		baseline := a.trafficBaselines[metadata.ID]
		upload := max(metadata.Upload.Load()-baseline.Upload, 0)
		download := max(metadata.Download.Load()-baseline.Download, 0)
		record := a.store.Inbounds[metadata.Metadata.Inbound]
		if record == nil {
			continue
		}
		for _, user := range record.Users {
			if user.Name == userName {
				user.UploadBytes += upload
				user.DownloadBytes += download
				user.UpdatedAt = time.Now().UnixMilli()
				break
			}
		}
		a.trafficBaselines[metadata.ID] = adminTrafficBaseline{
			Upload: metadata.Upload.Load(), Download: metadata.Download.Load(),
		}
	}
}

func adminUserKey(inbound string, name string) string {
	return inbound + "\x00" + name
}

func (a *adminAPI) buildRouter() http.Handler {
	router := chi.NewRouter()
	router.Use(a.authenticate)
	router.Get(adminRoutePrefix+"/overview", a.getOverview)
	router.Get(adminRoutePrefix+"/protocols", a.listProtocols)
	router.Get(adminRoutePrefix+"/servers", a.listServers)
	router.Post(adminRoutePrefix+"/servers", a.createServer)
	router.Put(adminRoutePrefix+"/servers/{tag}", a.updateServer)
	router.Delete(adminRoutePrefix+"/servers/{tag}", a.deleteServer)
	router.Post(adminRoutePrefix+"/reload", a.reloadCore)
	router.Get(adminRoutePrefix+"/users", a.listUsers)
	router.Post(adminRoutePrefix+"/users", a.createUser)
	router.Put(adminRoutePrefix+"/users/{id}", a.updateUser)
	router.Delete(adminRoutePrefix+"/users/{id}", a.deleteUser)
	router.Post(adminRoutePrefix+"/users/{id}/reset-traffic", a.resetUserTraffic)
	router.Get(adminRoutePrefix+"/connections", a.listConnections)
	router.Delete(adminRoutePrefix+"/connections", a.closeAllConnections)
	router.Delete(adminRoutePrefix+"/connections/{id}", a.closeConnection)
	return router
}

func (a *adminAPI) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	a.router.ServeHTTP(writer, request)
}

func (a *adminAPI) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if a.secret == "" {
			if !sameOriginAdminRequest(request) {
				writeAdminError(writer, http.StatusForbidden, "未設定 API Token 時只允許同源請求")
				return
			}
			next.ServeHTTP(writer, request)
			return
		}
		bearer, token, found := strings.Cut(request.Header.Get("Authorization"), " ")
		valid := found && bearer == "Bearer" && len(token) == len(a.secret) && subtle.ConstantTimeCompare([]byte(token), []byte(a.secret)) == 1
		if !valid {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeAdminError(writer, http.StatusUnauthorized, "API Token 無效")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func sameOriginAdminRequest(request *http.Request) bool {
	if request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Host == "" {
		return false
	}
	return strings.EqualFold(parsedOrigin.Host, request.Host)
}

func (a *adminAPI) getOverview(writer http.ResponseWriter, request *http.Request) {
	now := time.Now()
	active := a.activeUsage()
	summaries := a.inboundSummaries(now.UnixMilli(), active)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	var totalUsers, enabledUsers, disabledUsers, expiredUsers int
	a.storeAccess.RLock()
	for tag, record := range a.store.Inbounds {
		if a.runtimes[tag] == nil || a.runtimes[tag].Manager == nil {
			continue
		}
		for _, user := range record.Users {
			totalUsers++
			if !user.Enabled {
				disabledUsers++
			}
			if user.ExpiresAt > 0 && user.ExpiresAt <= now.UnixMilli() {
				expiredUsers++
			}
			if adminUserEnabled(user, now.UnixMilli(), active[adminUserKey(tag, user.Name)]) {
				enabledUsers++
			}
		}
	}
	a.storeAccess.RUnlock()
	var uploadTotal, downloadTotal int64
	var activeConnections int
	if a.traffic != nil {
		uploadTotal, downloadTotal = a.traffic.Total()
		activeConnections = a.traffic.ConnectionsLen()
	}
	uptime := time.Duration(0)
	if !a.startedAt.IsZero() {
		uptime = now.Sub(a.startedAt)
	}
	writeAdminJSON(writer, http.StatusOK, map[string]any{
		"version":        C.Version,
		"api_version":    daemon.APIVersion,
		"status":         "running",
		"started_at":     a.startedAt.UnixMilli(),
		"uptime_seconds": int64(uptime.Seconds()),
		"platform": map[string]any{
			"os":           runtime.GOOS,
			"arch":         runtime.GOARCH,
			"cpu_cores":    runtime.NumCPU(),
			"memory_bytes": memory.Sys,
			"goroutines":   runtime.NumGoroutine(),
		},
		"traffic": map[string]any{
			"uplink_total":       uploadTotal,
			"downlink_total":     downloadTotal,
			"active_connections": activeConnections,
		},
		"users": map[string]any{
			"total":    totalUsers,
			"enabled":  enabledUsers,
			"disabled": disabledUsers,
			"expired":  expiredUsers,
		},
		"authentication_enabled": a.secret != "",
		"inbounds":               summaries,
	})
}

func (a *adminAPI) inboundSummaries(now int64, active map[string]adminUsage) []adminInboundSummary {
	summaries := make([]adminInboundSummary, 0, len(a.inbounds))
	a.storeAccess.RLock()
	defer a.storeAccess.RUnlock()
	for _, runtimeInbound := range a.inbounds {
		summary := adminInboundSummary{Tag: runtimeInbound.Tag, Type: runtimeInbound.Type}
		if runtimeInbound.Manager != nil {
			schema := runtimeInbound.Manager.service.ManagedUserSchema()
			summary.Managed = true
			summary.Credential = schema.Credential
			summary.PasswordEncoding = schema.PasswordEncoding
			summary.PasswordBytes = schema.PasswordBytes
			summary.Flow = schema.Flow
			summary.AlterID = schema.AlterID
			summary.Traffic = !schema.NoTraffic
			if record := a.store.Inbounds[runtimeInbound.Tag]; record != nil {
				summary.UserCount = len(record.Users)
				for _, user := range record.Users {
					if adminUserEnabled(user, now, active[adminUserKey(runtimeInbound.Tag, user.Name)]) {
						summary.EnabledUserCount++
					}
				}
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func (a *adminAPI) listUsers(writer http.ResponseWriter, request *http.Request) {
	filterInbound := request.URL.Query().Get("inbound")
	active := a.activeUsage()
	views := make([]adminUserView, 0)
	a.storeAccess.RLock()
	for tag, record := range a.store.Inbounds {
		if filterInbound != "" && tag != filterInbound {
			continue
		}
		if runtimeInbound := a.runtimes[tag]; runtimeInbound == nil || runtimeInbound.Manager == nil {
			continue
		}
		for _, user := range record.Users {
			copyUser := *user
			usage := active[adminUserKey(tag, user.Name)]
			copyUser.UploadBytes += usage.Upload
			copyUser.DownloadBytes += usage.Download
			views = append(views, adminUserView{adminUser: copyUser, ActiveConnections: usage.Connections})
		}
	}
	a.storeAccess.RUnlock()
	sort.Slice(views, func(i, j int) bool {
		if views[i].Name == views[j].Name {
			return views[i].Inbound < views[j].Inbound
		}
		return strings.ToLower(views[i].Name) < strings.ToLower(views[j].Name)
	})
	writeAdminJSON(writer, http.StatusOK, map[string]any{
		"users":    views,
		"inbounds": a.inboundSummaries(time.Now().UnixMilli(), active),
	})
}

func (a *adminAPI) createUser(writer http.ResponseWriter, request *http.Request) {
	var input adminUserInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	a.mutation.Lock()
	defer a.mutation.Unlock()
	runtimeInbound := a.runtimes[input.Inbound]
	if runtimeInbound == nil || runtimeInbound.Manager == nil {
		writeAdminError(writer, http.StatusBadRequest, "此入站不支援動態用戶管理")
		return
	}
	normalized, err := normalizeAdminInput(input, runtimeInbound.Manager.service.ManagedUserSchema())
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, err.Error())
		return
	}
	id, err := newAdminID()
	if err != nil {
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UnixMilli()
	enabled := true
	if normalized.Enabled != nil {
		enabled = *normalized.Enabled
	}
	newUser := &adminUser{
		ID:         id,
		Inbound:    input.Inbound,
		Type:       runtimeInbound.Type,
		Name:       normalized.Name,
		UUID:       normalized.UUID,
		Password:   normalized.Password,
		Flow:       normalized.Flow,
		AlterID:    normalized.AlterID,
		Enabled:    enabled,
		QuotaBytes: normalized.QuotaBytes,
		ExpiresAt:  normalized.ExpiresAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	previous, err := a.mutateInbound(input.Inbound, func(record *adminInboundStore) error {
		if err := validateUniqueUser(record, newUser, ""); err != nil {
			return err
		}
		record.Authoritative = true
		record.Users = append(record.Users, newUser)
		return nil
	})
	if err != nil {
		writeAdminError(writer, http.StatusConflict, err.Error())
		return
	}
	if err = a.commitMutation(input.Inbound, previous); err != nil {
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(writer, http.StatusCreated, adminUserView{adminUser: *newUser})
}

func (a *adminAPI) updateUser(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	var input adminUserInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	a.mutation.Lock()
	defer a.mutation.Unlock()
	tag, current := a.findUser(id)
	if current == nil {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	if input.Inbound != "" && input.Inbound != tag {
		writeAdminError(writer, http.StatusBadRequest, "不可變更用戶所屬入站")
		return
	}
	runtimeInbound := a.runtimes[tag]
	if runtimeInbound == nil || runtimeInbound.Manager == nil {
		writeAdminError(writer, http.StatusNotFound, "用戶所屬入站已不存在")
		return
	}
	normalized, err := normalizeAdminInput(input, runtimeInbound.Manager.service.ManagedUserSchema())
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if normalized.Name != current.Name {
		a.trafficAccess.Lock()
		a.baselineUserTrafficLocked(tag, current.Name, true)
		a.trafficAccess.Unlock()
	}
	var updated adminUser
	previous, err := a.mutateInbound(tag, func(record *adminInboundStore) error {
		for index, user := range record.Users {
			if user.ID == id {
				record.Authoritative = true
				updated = *user
				updated.Name = normalized.Name
				updated.UUID = normalized.UUID
				updated.Password = normalized.Password
				updated.Flow = normalized.Flow
				updated.AlterID = normalized.AlterID
				updated.QuotaBytes = normalized.QuotaBytes
				updated.ExpiresAt = normalized.ExpiresAt
				updated.UpdatedAt = time.Now().UnixMilli()
				if normalized.Enabled != nil {
					updated.Enabled = *normalized.Enabled
				}
				if err := validateUniqueUser(record, &updated, id); err != nil {
					return err
				}
				record.Users[index] = &updated
				return nil
			}
		}
		return os.ErrNotExist
	})
	if err != nil {
		writeAdminError(writer, http.StatusConflict, err.Error())
		return
	}
	if err = a.commitMutation(tag, previous); err != nil {
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(writer, http.StatusOK, adminUserView{adminUser: updated})
}

func (a *adminAPI) deleteUser(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	a.mutation.Lock()
	defer a.mutation.Unlock()
	tag, current := a.findUser(id)
	if current == nil {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	a.trafficAccess.Lock()
	a.baselineUserTrafficLocked(tag, current.Name, false)
	a.trafficAccess.Unlock()
	previous, err := a.mutateInbound(tag, func(record *adminInboundStore) error {
		for index, user := range record.Users {
			if user.ID == id {
				record.Users = append(record.Users[:index], record.Users[index+1:]...)
				record.Authoritative = true
				return nil
			}
		}
		return os.ErrNotExist
	})
	if err != nil {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	if err = a.commitMutation(tag, previous); err != nil {
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) resetUserTraffic(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	a.mutation.Lock()
	defer a.mutation.Unlock()
	tag, current := a.findUser(id)
	if current == nil {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	a.trafficAccess.Lock()
	previous, err := a.mutateInbound(tag, func(record *adminInboundStore) error {
		for _, user := range record.Users {
			if user.ID == id {
				user.UploadBytes = 0
				user.DownloadBytes = 0
				user.UpdatedAt = time.Now().UnixMilli()
				return nil
			}
		}
		return os.ErrNotExist
	})
	if err == nil {
		a.baselineUserTrafficLocked(tag, current.Name, false)
	}
	a.trafficAccess.Unlock()
	if err != nil {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	if err = a.commitMutation(tag, previous); err != nil {
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) mutateInbound(tag string, mutate func(record *adminInboundStore) error) (*adminInboundStore, error) {
	a.storeAccess.Lock()
	defer a.storeAccess.Unlock()
	record := a.store.Inbounds[tag]
	if record == nil {
		return nil, os.ErrNotExist
	}
	previous := cloneInboundStore(record)
	if err := mutate(record); err != nil {
		return nil, err
	}
	return previous, nil
}

func (a *adminAPI) commitMutation(tag string, previous *adminInboundStore) error {
	if err := a.applyInbound(tag, true); err != nil {
		rollbackErr := a.rollbackMutation(tag, previous, false)
		return errors.Join(E.Cause(err, "更新核心用戶失敗"), rollbackErr)
	}
	if err := a.saveStore(); err != nil {
		rollbackErr := a.rollbackMutation(tag, previous, true)
		return errors.Join(E.Cause(err, "儲存用戶資料失敗"), rollbackErr)
	}
	return nil
}

func (a *adminAPI) rollbackMutation(tag string, previous *adminInboundStore, persist bool) error {
	a.storeAccess.Lock()
	a.store.Inbounds[tag] = previous
	a.storeAccess.Unlock()
	applyErr := a.applyInbound(tag, true)
	var saveErr error
	if persist {
		saveErr = a.saveStore()
	}
	return errors.Join(applyErr, saveErr)
}

func cloneInboundStore(record *adminInboundStore) *adminInboundStore {
	copyRecord := *record
	copyRecord.Users = make([]*adminUser, len(record.Users))
	for index, user := range record.Users {
		copyUser := *user
		copyRecord.Users[index] = &copyUser
	}
	return &copyRecord
}

func (a *adminAPI) findUser(id string) (string, *adminUser) {
	a.storeAccess.RLock()
	defer a.storeAccess.RUnlock()
	for tag, record := range a.store.Inbounds {
		runtimeInbound := a.runtimes[tag]
		if runtimeInbound == nil || runtimeInbound.Manager == nil {
			continue
		}
		for _, user := range record.Users {
			if user.ID == id {
				copyUser := *user
				return tag, &copyUser
			}
		}
	}
	return "", nil
}

func normalizeAdminInput(input adminUserInput, schema adapter.ManagedUserSchema) (adminUserInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return input, E.New("用戶名稱不能留空")
	}
	if len(input.Name) > 128 {
		return input, E.New("用戶名稱過長")
	}
	if input.QuotaBytes < 0 || input.ExpiresAt < 0 || input.AlterID < 0 {
		return input, E.New("額度、到期時間與 Alter ID 不可為負數")
	}
	switch schema.Credential {
	case adapter.ManagedUserCredentialUUID, adapter.ManagedUserCredentialUUIDPassword:
		parsedUUID, err := uuid.FromString(strings.TrimSpace(input.UUID))
		if err != nil {
			return input, E.New("UUID 格式不正確")
		}
		input.UUID = parsedUUID.String()
	default:
		input.UUID = ""
	}
	if schema.Credential == adapter.ManagedUserCredentialPassword || schema.Credential == adapter.ManagedUserCredentialUUIDPassword {
		if input.Password == "" {
			return input, E.New("密碼不能留空")
		}
		if len(input.Password) > 1024 {
			return input, E.New("密碼過長")
		}
		if schema.PasswordEncoding == adapter.ManagedUserPasswordBase64 && schema.PasswordBytes > 0 {
			decoded, err := base64.StdEncoding.DecodeString(input.Password)
			if err != nil || len(decoded) != schema.PasswordBytes {
				return input, E.New("密碼必須是 ", schema.PasswordBytes, " bytes 的標準 Base64")
			}
		}
	} else {
		input.Password = ""
	}
	if !schema.Flow {
		input.Flow = ""
	}
	if !schema.AlterID {
		input.AlterID = 0
	}
	if schema.NoTraffic {
		input.QuotaBytes = 0
	}
	return input, nil
}

func validateUniqueUser(record *adminInboundStore, candidate *adminUser, skipID string) error {
	for _, user := range record.Users {
		if user.ID == skipID {
			continue
		}
		if strings.EqualFold(user.Name, candidate.Name) {
			return E.New("用戶名稱已存在")
		}
		if candidate.UUID != "" && user.UUID == candidate.UUID {
			return E.New("UUID 已存在")
		}
		if candidate.Password != "" && user.Password == candidate.Password {
			return E.New("密碼已被其他用戶使用")
		}
	}
	return nil
}

type adminConnection struct {
	ID            string `json:"id"`
	Inbound       string `json:"inbound"`
	InboundType   string `json:"inbound_type"`
	User          string `json:"user"`
	Source        string `json:"source"`
	Destination   string `json:"destination"`
	Network       string `json:"network"`
	Protocol      string `json:"protocol"`
	Outbound      string `json:"outbound"`
	CreatedAt     int64  `json:"created_at"`
	UploadBytes   int64  `json:"upload_bytes"`
	DownloadBytes int64  `json:"download_bytes"`
}

func (a *adminAPI) listConnections(writer http.ResponseWriter, request *http.Request) {
	connections := make([]adminConnection, 0)
	if a.traffic != nil {
		for _, metadata := range a.traffic.Connections() {
			connections = append(connections, adminConnection{
				ID:            metadata.ID.String(),
				Inbound:       metadata.Metadata.Inbound,
				InboundType:   metadata.Metadata.InboundType,
				User:          metadata.Metadata.User,
				Source:        metadata.Metadata.Source.String(),
				Destination:   metadata.Metadata.Destination.String(),
				Network:       metadata.Metadata.Network,
				Protocol:      metadata.Metadata.Protocol,
				Outbound:      metadata.Outbound,
				CreatedAt:     metadata.CreatedAt.UnixMilli(),
				UploadBytes:   metadata.Upload.Load(),
				DownloadBytes: metadata.Download.Load(),
			})
		}
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].CreatedAt > connections[j].CreatedAt })
	writeAdminJSON(writer, http.StatusOK, map[string]any{"connections": connections})
}

func (a *adminAPI) closeConnection(writer http.ResponseWriter, request *http.Request) {
	if a.traffic == nil {
		writeAdminError(writer, http.StatusNotFound, "找不到連線")
		return
	}
	id, err := uuid.FromString(chi.URLParam(request, "id"))
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, "連線 ID 格式不正確")
		return
	}
	connection := a.traffic.Connection(id)
	if connection == nil {
		writeAdminError(writer, http.StatusNotFound, "找不到連線")
		return
	}
	if err = connection.Close(); err != nil {
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) closeAllConnections(writer http.ResponseWriter, request *http.Request) {
	if a.traffic != nil {
		a.traffic.CloseAllConnections()
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) saveStore() error {
	a.saveAccess.Lock()
	defer a.saveAccess.Unlock()
	a.storeAccess.RLock()
	content, err := json.MarshalIndent(a.store, "", "  ")
	a.storeAccess.RUnlock()
	if err != nil {
		return err
	}
	parent := filepath.Dir(a.dataPath)
	if parent != "." {
		if err = filemanager.MkdirAll(a.ctx, parent, 0o700); err != nil {
			return err
		}
	}
	tempPath := a.dataPath + ".tmp"
	backupPath := a.dataPath + ".bak"
	if err = filemanager.WriteFile(a.ctx, tempPath, append(content, '\n'), 0o600); err != nil {
		return err
	}
	_ = filemanager.Remove(a.ctx, backupPath)
	_, statErr := filemanager.Stat(a.ctx, a.dataPath)
	hasCurrent := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		_ = filemanager.Remove(a.ctx, tempPath)
		return statErr
	}
	if hasCurrent {
		if err = filemanager.Rename(a.ctx, a.dataPath, backupPath); err != nil {
			_ = filemanager.Remove(a.ctx, tempPath)
			return err
		}
	}
	if err = filemanager.Rename(a.ctx, tempPath, a.dataPath); err != nil {
		if hasCurrent {
			_ = filemanager.Rename(a.ctx, backupPath, a.dataPath)
		}
		_ = filemanager.Remove(a.ctx, tempPath)
		return err
	}
	if hasCurrent {
		_ = filemanager.Remove(a.ctx, backupPath)
	}
	return nil
}

func decodeAdminJSON(writer http.ResponseWriter, request *http.Request, value any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeAdminError(writer, http.StatusBadRequest, "請求內容格式不正確")
		return err
	}
	return nil
}

func writeAdminJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeAdminError(writer http.ResponseWriter, status int, message string) {
	writeAdminJSON(writer, status, map[string]string{"error": message})
}
