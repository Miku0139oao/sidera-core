package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/dashboardstore"
	"github.com/Miku0139oao/sidera-core/common/trafficcontrol"
	"github.com/Miku0139oao/sidera-core/common/validation"
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
	adminRoutePrefix                = "/api/admin"
	externalSubscriptionRoutePrefix = "/sub/"
	subscriptionRoutePrefix         = "/sub/sidera/"
	profilePageRoutePrefix          = "/api/list/nodes/"
	adminStoreVersion               = dashboardstore.StoreVersion
	adminAccountPolicyMembership    = "membership_local"
	adminAccountPolicyGlobal        = "account_global"
	adminDayMilliseconds            = int64(24 * time.Hour / time.Millisecond)
	adminMaintenanceInterval        = 2 * time.Second
	adminTrafficCheckpointInterval  = 30 * time.Second
	adminOverviewCacheDuration      = time.Second
)

// ContextWithValidation disables runtime and persistent dashboard mutations
// while a Box is constructed only to validate configuration.
func ContextWithValidation(ctx context.Context) context.Context {
	return validation.Context(ctx)
}

func ResolveDashboardDataPath(ctx context.Context, dataPath string) string {
	return dashboardstore.ResolveDataPath(ctx, dataPath)
}

type adminAPI struct {
	ctx            context.Context
	logger         log.ContextLogger
	secret         string
	dataPath       string
	publicBaseURL  string
	secure         bool
	router         http.Handler
	startedAt      time.Time
	validationOnly bool

	traffic             *trafficcontrol.Manager
	runtimes            map[string]*adminInboundRuntime
	inbounds            []adminInboundRuntime
	serverRevisions     map[string]int64
	userAliases         map[string]adminManagedUserIdentity
	processSignalReload bool

	storeAccess           sync.RWMutex
	store                 adminStore
	mutation              sync.Mutex
	saveAccess            sync.Mutex
	trafficAccess         sync.Mutex
	trafficBaselines      map[uuid.UUID]adminTrafficBaseline
	pendingTrafficAccess  sync.Mutex
	pendingTraffic        []adminTrafficEvent
	runCtx                context.Context
	cancel                context.CancelFunc
	workers               sync.WaitGroup
	handlerAccess         sync.Mutex
	handlers              sync.WaitGroup
	closing               bool
	overviewAccess        sync.Mutex
	overviewContent       []byte
	overviewExpires       time.Time
	removeTrafficOpenHook func()
	removeTrafficHook     func()
	dirty                 atomic.Bool
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
	Version               int                           `json:"version"`
	Accounts              map[string]*adminAccount      `json:"accounts,omitempty"`
	Inbounds              map[string]*adminInboundStore `json:"inbounds"`
	Servers               map[string]*adminServerStore  `json:"servers,omitempty"`
	Subscriptions         map[string]string             `json:"subscriptions,omitempty"`
	ExternalSubscriptions map[string]string             `json:"external_subscriptions,omitempty"`
	Settings              adminSettings                 `json:"settings,omitempty"`
}

type adminAccount struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	PolicyScope       string `json:"policy_scope"`
	Enabled           bool   `json:"enabled"`
	QuotaBytes        int64  `json:"quota_bytes"`
	ExpiresAt         int64  `json:"expires_at"`
	MaxIPs            int    `json:"max_ips,omitempty"`
	ResetDays         int    `json:"reset_days,omitempty"`
	BaseUploadBytes   int64  `json:"base_upload_bytes,omitempty"`
	BaseDownloadBytes int64  `json:"base_download_bytes,omitempty"`
	LastOnline        int64  `json:"last_online,omitempty"`
	Revision          int64  `json:"revision"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`
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
	Type            string       `json:"type"`
	Authoritative   bool         `json:"authoritative"`
	BlockUUID       string       `json:"block_uuid"`
	BlockPassword   string       `json:"block_password"`
	Users           []*adminUser `json:"users"`
	Revision        int64        `json:"revision,omitempty"`
	AppliedRevision int64        `json:"applied_revision,omitempty"`
}

type adminUser struct {
	ID                string `json:"id"`
	AccountID         string `json:"account_id"`
	Inbound           string `json:"inbound"`
	Type              string `json:"type"`
	Name              string `json:"name"`
	UUID              string `json:"uuid,omitempty"`
	Password          string `json:"password,omitempty"`
	Flow              string `json:"flow,omitempty"`
	AlterID           int    `json:"alter_id,omitempty"`
	Enabled           bool   `json:"enabled"`
	QuotaBytes        int64  `json:"quota_bytes"`
	ExpiresAt         int64  `json:"expires_at"`
	MaxIPs            int    `json:"max_ips,omitempty"`
	UploadBytes       int64  `json:"upload_bytes"`
	DownloadBytes     int64  `json:"download_bytes"`
	TrafficGeneration int64  `json:"traffic_generation,omitempty"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`
}

type adminUserView struct {
	adminUser
	ActiveConnections int             `json:"active_connections"`
	OnlineIPs         []adminOnlineIP `json:"online_ips,omitempty"`
	Revision          int64           `json:"revision"`
	AppliedRevision   int64           `json:"applied_revision"`
	SubscriptionURL   string          `json:"subscription_url,omitempty"`
}

type adminAccountView struct {
	adminAccount
	UploadBytes       int64           `json:"upload_bytes"`
	DownloadBytes     int64           `json:"download_bytes"`
	ActiveConnections int             `json:"active_connections"`
	OnlineIPs         []adminOnlineIP `json:"online_ips,omitempty"`
}

type adminOnlineIP struct {
	Address string `json:"address"`
	Since   int64  `json:"since"`
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
	MaxIPs     *int   `json:"max_ips"`
	Revision   int64  `json:"revision"`
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
	Revision         int64  `json:"revision,omitempty"`
	AppliedRevision  int64  `json:"applied_revision,omitempty"`
	Pending          bool   `json:"pending,omitempty"`
}

type adminUsage struct {
	Upload      int64
	Download    int64
	Connections int
	SourceSince map[string]int64
}

type adminTrafficBaseline struct {
	Upload     int64
	Download   int64
	UserID     string
	Generation int64
}

type adminTrafficEvent struct {
	Inbound    string
	User       string
	UserID     string
	Generation int64
	Upload     int64
	Download   int64
	UpdatedAt  int64
}

type adminManagedUserIdentity struct {
	ID         string
	Generation int64
}

func newAdminAPI(ctx context.Context, logger log.ContextLogger, secret string, dataPath string, publicBaseURL string, serverRevisions map[string]int64, processSignalReload bool) (*adminAPI, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a := &adminAPI{
		ctx:                 ctx,
		runCtx:              runCtx,
		cancel:              cancel,
		logger:              logger,
		secret:              secret,
		dataPath:            ResolveDashboardDataPath(ctx, dataPath),
		publicBaseURL:       publicBaseURL,
		validationOnly:      validation.Only(ctx),
		traffic:             service.PtrFromContext[trafficcontrol.Manager](ctx),
		runtimes:            make(map[string]*adminInboundRuntime),
		serverRevisions:     make(map[string]int64, len(serverRevisions)),
		userAliases:         make(map[string]adminManagedUserIdentity),
		processSignalReload: processSignalReload,
		trafficBaselines:    make(map[uuid.UUID]adminTrafficBaseline),
		store: adminStore{
			Version:               adminStoreVersion,
			Accounts:              make(map[string]*adminAccount),
			Inbounds:              make(map[string]*adminInboundStore),
			Servers:               make(map[string]*adminServerStore),
			Subscriptions:         make(map[string]string),
			ExternalSubscriptions: make(map[string]string),
			Settings:              defaultAdminSettings(),
		},
	}
	maps.Copy(a.serverRevisions, serverRevisions)
	a.discoverServices(ctx)
	if err := a.loadStore(); err != nil {
		cancel()
		return nil, err
	}
	if err := a.synchronizeStore(); err != nil {
		cancel()
		return nil, err
	}
	if err := a.ensureSubscriptionTokens(); err != nil {
		cancel()
		return nil, E.Cause(err, "initialize subscription tokens")
	}
	if err := a.applyAll(true); err != nil {
		cancel()
		return nil, E.Cause(err, "apply dashboard users")
	}
	if a.validationOnly {
		if err := a.validateStoreWritable(); err != nil {
			cancel()
			return nil, E.Cause(err, "validate dashboard data path")
		}
	} else {
		if err := a.saveStore(); err != nil {
			cancel()
			return nil, E.Cause(err, "save dashboard users")
		}
	}
	a.router = a.buildRouter()
	return a, nil
}

func (a *adminAPI) validateStoreWritable() error {
	if info, err := os.Stat(a.dataPath); err == nil {
		if info.IsDir() {
			return E.New("dashboard data path is a directory: ", a.dataPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	parent := filepath.Dir(a.dataPath)
	for {
		info, err := os.Stat(parent)
		if err == nil {
			if !info.IsDir() {
				return E.New("dashboard data parent is not a directory: ", parent)
			}
			probe, createErr := os.CreateTemp(parent, ".sidera-dashboard-check-*")
			if createErr != nil {
				return createErr
			}
			probePath := probe.Name()
			renamedPath := probePath + ".renamed"
			_, writeErr := probe.Write([]byte("sidera-dashboard-check"))
			syncErr := probe.Sync()
			closeErr := probe.Close()
			if writeErr != nil || syncErr != nil || closeErr != nil {
				_ = os.Remove(probePath)
				return errors.Join(writeErr, syncErr, closeErr)
			}
			renameErr := os.Rename(probePath, renamedPath)
			if renameErr != nil {
				_ = os.Remove(probePath)
				return renameErr
			}
			return os.Remove(renamedPath)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return err
		}
		parent = next
	}
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
	var result error
	found := false
	for _, candidatePath := range []string{a.dataPath, a.dataPath + ".bak"} {
		content, err := filemanager.ReadFile(a.ctx, candidatePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		found = true
		if err == nil {
			var stored adminStore
			stored, err = decodeAdminStore(content, time.Now().UnixMilli())
			if err == nil {
				a.store = stored
				if candidatePath != a.dataPath {
					a.logger.Warn("dashboard: recovered data from backup after primary store failed")
				}
				return nil
			}
		}
		result = errors.Join(result, E.Cause(err, "load dashboard data at ", candidatePath))
	}
	if !found {
		return nil
	}
	return result
}

func decodeAdminStore(content []byte, now int64) (adminStore, error) {
	var stored adminStore
	if err := json.Unmarshal(content, &stored); err != nil {
		return adminStore{}, E.Cause(err, "decode dashboard data")
	}
	sourceVersion := stored.Version
	if sourceVersion == 1 || sourceVersion == 2 || sourceVersion == 3 || sourceVersion == 4 {
		stored.Settings = defaultAdminSettings()
	}
	if sourceVersion < 1 || sourceVersion > adminStoreVersion {
		return adminStore{}, E.New("unsupported dashboard data version: ", stored.Version)
	}
	stored.Version = adminStoreVersion
	if stored.Accounts == nil {
		stored.Accounts = make(map[string]*adminAccount)
	}
	if stored.Inbounds == nil {
		stored.Inbounds = make(map[string]*adminInboundStore)
	}
	if stored.Servers == nil {
		stored.Servers = make(map[string]*adminServerStore)
	}
	if stored.Subscriptions == nil {
		stored.Subscriptions = make(map[string]string)
	}
	if stored.ExternalSubscriptions == nil {
		stored.ExternalSubscriptions = make(map[string]string)
	}
	for tag, record := range stored.Inbounds {
		if record == nil {
			return adminStore{}, E.New("invalid null dashboard inbound: ", tag)
		}
		for index, user := range record.Users {
			if user == nil {
				return adminStore{}, E.New("invalid null dashboard user in ", tag, " at index ", index)
			}
		}
	}
	if err := ensureAdminAccounts(&stored, now); err != nil {
		return adminStore{}, err
	}
	return stored, nil
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
			a.store.Inbounds[runtimeInbound.Tag] = record
		}
		if !record.Authoritative {
			changed, err := mirrorManagedUsers(record, runtimeInbound.Tag, runtimeInbound.Type, runtimeInbound.Manager.service.ManagedUsers(), now)
			if err != nil {
				return err
			}
			if changed || record.Revision == 0 {
				record.Revision = nextAdminRevision(record.Revision, now)
			}
			record.AppliedRevision = record.Revision
			record.Authoritative = dashboardOwned
		} else if record.Revision == 0 {
			record.Revision = nextAdminRevision(0, now)
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
	return ensureAdminAccounts(&a.store, now)
}

func ensureAdminAccounts(store *adminStore, now int64) error {
	if store.Accounts == nil {
		store.Accounts = make(map[string]*adminAccount)
	}
	byName := make(map[string]*adminAccount, len(store.Accounts))
	for identifier, account := range store.Accounts {
		if account == nil || identifier == "" || account.ID != identifier || account.Name == "" {
			return E.New("invalid dashboard account: ", identifier)
		}
		if account.PolicyScope != adminAccountPolicyMembership && account.PolicyScope != adminAccountPolicyGlobal {
			return E.New("invalid dashboard account policy: ", identifier)
		}
		if account.QuotaBytes < 0 || account.ExpiresAt < 0 || account.MaxIPs < 0 || account.ResetDays < 0 || int64(account.ResetDays) > math.MaxInt64/adminDayMilliseconds || account.BaseUploadBytes < 0 || account.BaseDownloadBytes < 0 || account.LastOnline < 0 {
			return E.New("invalid dashboard account limits: ", identifier)
		}
		if byName[account.Name] != nil {
			return E.New("duplicate dashboard account name: ", account.Name)
		}
		byName[account.Name] = account
	}

	type accountReference struct {
		users []*adminUser
		names map[string]bool
	}
	references := make(map[string]*accountReference)
	for _, inbound := range store.Inbounds {
		if inbound == nil {
			continue
		}
		for _, user := range inbound.Users {
			if user == nil || user.AccountID == "" {
				continue
			}
			if store.Accounts[user.AccountID] == nil {
				return E.New("dashboard user references missing account: ", user.AccountID)
			}
			reference := references[user.AccountID]
			if reference == nil {
				reference = &accountReference{names: make(map[string]bool)}
				references[user.AccountID] = reference
			}
			reference.users = append(reference.users, user)
			reference.names[user.Name] = true
		}
	}
	for identifier, reference := range references {
		account := store.Accounts[identifier]
		if len(reference.names) == 1 {
			for name := range reference.names {
				if name == account.Name {
					break
				}
				if existing := byName[name]; existing == nil {
					delete(byName, account.Name)
					account.Name = name
					account.UpdatedAt = now
					account.Revision = nextAdminRevision(account.Revision, now)
					byName[name] = account
				} else {
					for _, user := range reference.users {
						user.AccountID = existing.ID
					}
				}
			}
			continue
		}
		for _, user := range reference.users {
			if user.Name != account.Name {
				user.AccountID = ""
			}
		}
	}

	used := make(map[string]bool)
	for _, inbound := range store.Inbounds {
		if inbound == nil {
			continue
		}
		for _, user := range inbound.Users {
			if user == nil {
				continue
			}
			if user.AccountID == "" {
				account := byName[user.Name]
				if account == nil {
					identifier, err := newAdminID()
					if err != nil {
						return err
					}
					createdAt := user.CreatedAt
					if createdAt == 0 {
						createdAt = now
					}
					account = &adminAccount{
						ID: identifier, Name: user.Name, PolicyScope: adminAccountPolicyMembership, Enabled: true,
						Revision: nextAdminRevision(0, now), CreatedAt: createdAt, UpdatedAt: max(user.UpdatedAt, createdAt),
					}
					store.Accounts[identifier] = account
					byName[user.Name] = account
				}
				user.AccountID = account.ID
			}
			used[user.AccountID] = true
		}
	}
	for identifier := range store.Accounts {
		if !used[identifier] {
			delete(store.Accounts, identifier)
		}
	}
	return nil
}

func mirrorManagedUsers(record *adminInboundStore, tag string, inboundType string, runtimeUsers []adapter.ManagedUser, now int64) (bool, error) {
	existingByName := make(map[string]*adminUser, len(record.Users))
	for _, user := range record.Users {
		existingByName[user.Name] = user
	}
	seen := make(map[string]bool, len(runtimeUsers))
	users := make([]*adminUser, 0, len(runtimeUsers))
	changed := len(record.Users) != len(runtimeUsers)
	for _, runtimeUser := range runtimeUsers {
		if seen[runtimeUser.Name] {
			return false, E.New("duplicate runtime user name: ", runtimeUser.Name)
		}
		seen[runtimeUser.Name] = true
		current := existingByName[runtimeUser.Name]
		userChanged := false
		if current == nil {
			id, err := newAdminID()
			if err != nil {
				return false, err
			}
			current = &adminUser{ID: id, CreatedAt: now}
			userChanged = true
		} else {
			copyUser := *current
			current = &copyUser
		}
		if current.Inbound != tag || current.Type != inboundType || current.Name != runtimeUser.Name ||
			current.UUID != runtimeUser.UUID || current.Password != runtimeUser.Password ||
			current.Flow != runtimeUser.Flow || current.AlterID != runtimeUser.AlterID || !current.Enabled {
			userChanged = true
		}
		current.Inbound = tag
		current.Type = inboundType
		current.Name = runtimeUser.Name
		current.UUID = runtimeUser.UUID
		current.Password = runtimeUser.Password
		current.Flow = runtimeUser.Flow
		current.AlterID = runtimeUser.AlterID
		current.Enabled = true
		if userChanged {
			current.UpdatedAt = now
		}
		changed = changed || userChanged
		users = append(users, current)
	}
	record.Users = users
	return changed, nil
}

func nextAdminRevision(current int64, now int64) int64 {
	return max(current+1, now)
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
	if a.validationOnly {
		return nil
	}
	a.startedAt = time.Now()
	if a.traffic != nil {
		a.removeTrafficOpenHook = a.traffic.AddOpenHook(a.recordTrafficOpen)
		a.removeTrafficHook = a.traffic.AddCloseHook(a.recordTraffic)
		for _, metadata := range a.traffic.Connections() {
			a.recordTrafficOpen(metadata)
		}
	}
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		a.maintenanceLoop()
	}()
	return nil
}

func (a *adminAPI) markServerProfilesApplied() (func() error, error) {
	a.mutation.Lock()
	defer a.unlockMutation()
	changed := false
	a.storeAccess.Lock()
	previousServers := cloneAdminServerStores(a.store.Servers)
	previousInbounds := cloneAdminInboundStores(a.store.Inbounds)
	for tag, profile := range a.store.Servers {
		expectedRevision, expected := a.serverRevisions[tag]
		if !expected || expectedRevision != profile.Revision {
			continue
		}
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
	if !changed {
		return nil, nil
	}
	if err := a.saveStore(); err != nil {
		a.restoreAppliedServerProfiles(previousServers, previousInbounds)
		return nil, err
	}
	return func() error {
		a.mutation.Lock()
		defer a.unlockMutation()
		a.restoreAppliedServerProfiles(previousServers, previousInbounds)
		return a.saveStore()
	}, nil
}

func (a *adminAPI) restoreAppliedServerProfiles(servers map[string]*adminServerStore, inbounds map[string]*adminInboundStore) {
	a.storeAccess.Lock()
	a.store.Servers = servers
	a.store.Inbounds = inbounds
	a.storeAccess.Unlock()
}

func (a *adminAPI) close() {
	a.handlerAccess.Lock()
	a.closing = true
	a.handlerAccess.Unlock()
	a.handlers.Wait()
	if a.removeTrafficOpenHook != nil {
		a.removeTrafficOpenHook()
		a.removeTrafficOpenHook = nil
	}
	if a.removeTrafficHook != nil {
		a.removeTrafficHook()
		a.removeTrafficHook = nil
	}
	a.cancel()
	a.workers.Wait()
	if a.validationOnly {
		return
	}
	a.snapshotActiveTraffic()
	if err := a.saveStore(); err != nil {
		a.logger.Error("dashboard: save data: ", err)
	}
}

func (a *adminAPI) recordTrafficOpen(metadata *trafficcontrol.TrackerMetadata) {
	userName := metadata.Metadata.User
	if userName == "" {
		return
	}
	userID, generation := a.managedUserIdentity(metadata.Metadata.Inbound, userName)
	a.trafficAccess.Lock()
	baseline := a.trafficBaselines[metadata.ID]
	baseline.UserID = userID
	baseline.Generation = generation
	a.trafficBaselines[metadata.ID] = baseline
	a.trafficAccess.Unlock()
}

func (a *adminAPI) recordTraffic(metadata *trafficcontrol.TrackerMetadata) {
	userName := metadata.Metadata.User
	if userName == "" {
		return
	}
	a.trafficAccess.Lock()
	baseline := a.trafficBaselines[metadata.ID]
	delete(a.trafficBaselines, metadata.ID)
	upload := max(metadata.Upload.Load()-baseline.Upload, 0)
	download := max(metadata.Download.Load()-baseline.Download, 0)
	if baseline.UserID == "" {
		baseline.UserID, baseline.Generation = a.managedUserIdentity(metadata.Metadata.Inbound, userName)
	}
	a.trafficAccess.Unlock()
	if upload == 0 && download == 0 {
		return
	}
	event := adminTrafficEvent{
		Inbound: metadata.Metadata.Inbound, User: userName,
		UserID: baseline.UserID, Generation: baseline.Generation,
		Upload: upload, Download: download, UpdatedAt: time.Now().UnixMilli(),
	}
	if !a.mutation.TryLock() {
		a.pendingTrafficAccess.Lock()
		a.pendingTraffic = append(a.pendingTraffic, event)
		a.pendingTrafficAccess.Unlock()
		a.dirty.Store(true)
		return
	}
	a.flushPendingTrafficLocked()
	a.applyTrafficEventsLocked([]adminTrafficEvent{event})
	a.mutation.Unlock()
}

func (a *adminAPI) applyTrafficEventsLocked(events []adminTrafficEvent) {
	if len(events) == 0 {
		return
	}
	a.storeAccess.Lock()
	defer a.storeAccess.Unlock()
	if len(events) == 1 {
		a.applyTrafficEventLocked(events[0])
		return
	}
	type trafficEventKey struct {
		identity   string
		generation int64
		byID       bool
	}
	aggregated := make(map[string]map[trafficEventKey]adminTrafficEvent)
	for _, event := range events {
		identity := event.User
		byID := event.UserID != ""
		if byID {
			identity = event.UserID
		}
		byIdentity := aggregated[event.Inbound]
		if byIdentity == nil {
			byIdentity = make(map[trafficEventKey]adminTrafficEvent)
			aggregated[event.Inbound] = byIdentity
		}
		key := trafficEventKey{identity: identity, generation: event.Generation, byID: byID}
		current := byIdentity[key]
		current.Inbound = event.Inbound
		current.User = event.User
		current.UserID = event.UserID
		current.Generation = event.Generation
		current.Upload = saturatingAdminTrafficAdd(current.Upload, event.Upload)
		current.Download = saturatingAdminTrafficAdd(current.Download, event.Download)
		current.UpdatedAt = max(current.UpdatedAt, event.UpdatedAt)
		byIdentity[key] = current
	}
	for inbound, byIdentity := range aggregated {
		record := a.store.Inbounds[inbound]
		if record == nil {
			continue
		}
		for _, user := range record.Users {
			if event, exists := byIdentity[trafficEventKey{identity: user.ID, generation: user.TrafficGeneration, byID: true}]; exists {
				a.applyTrafficToUserLocked(user, event)
			}
			if event, exists := byIdentity[trafficEventKey{identity: user.Name, generation: user.TrafficGeneration}]; exists {
				a.applyTrafficToUserLocked(user, event)
			}
		}
	}
}

func (a *adminAPI) applyTrafficEventLocked(event adminTrafficEvent) {
	record := a.store.Inbounds[event.Inbound]
	if record == nil {
		return
	}
	for _, user := range record.Users {
		matches := (event.UserID != "" && user.ID == event.UserID) || (event.UserID == "" && user.Name == event.User)
		if matches && user.TrafficGeneration == event.Generation {
			a.applyTrafficToUserLocked(user, event)
			return
		}
	}
}

func (a *adminAPI) applyTrafficToUserLocked(user *adminUser, event adminTrafficEvent) {
	user.UploadBytes = saturatingAdminTrafficAdd(user.UploadBytes, event.Upload)
	user.DownloadBytes = saturatingAdminTrafficAdd(user.DownloadBytes, event.Download)
	user.UpdatedAt = event.UpdatedAt
	if account := a.store.Accounts[user.AccountID]; account != nil && account.PolicyScope == adminAccountPolicyGlobal && event.UpdatedAt > account.LastOnline {
		account.LastOnline = event.UpdatedAt
		account.UpdatedAt = event.UpdatedAt
	}
	a.dirty.Store(true)
}

func (a *adminAPI) managedUserIdentity(tag string, name string) (string, int64) {
	a.storeAccess.RLock()
	defer a.storeAccess.RUnlock()
	record := a.store.Inbounds[tag]
	if record == nil {
		return "", 0
	}
	for _, user := range record.Users {
		if user.Name == name {
			return user.ID, user.TrafficGeneration
		}
	}
	alias := a.userAliases[adminUserKey(tag, name)]
	return alias.ID, alias.Generation
}

func (a *adminAPI) flushPendingTraffic() {
	a.mutation.Lock()
	a.flushPendingTrafficLocked()
	a.mutation.Unlock()
}

func (a *adminAPI) flushPendingTrafficLocked() {
	a.pendingTrafficAccess.Lock()
	events := a.pendingTraffic
	a.pendingTraffic = nil
	a.pendingTrafficAccess.Unlock()
	a.applyTrafficEventsLocked(events)
}

func (a *adminAPI) unlockMutation() {
	a.flushPendingTrafficLocked()
	a.mutation.Unlock()
}

func (a *adminAPI) maintenanceLoop() {
	maintenanceTicker := time.NewTicker(adminMaintenanceInterval)
	checkpointTicker := time.NewTicker(adminTrafficCheckpointInterval)
	defer maintenanceTicker.Stop()
	defer checkpointTicker.Stop()
	for {
		select {
		case <-a.runCtx.Done():
			return
		case <-checkpointTicker.C:
			a.checkpointTraffic()
			continue
		case <-maintenanceTicker.C:
		}
		a.flushPendingTraffic()
		if err := a.renewExpiredAccounts(time.Now().UnixMilli()); err != nil {
			a.logger.Error("dashboard: renew accounts: ", err)
		}
		if err := a.reconcile(); err != nil {
			a.logger.Error("dashboard: reconcile users: ", err)
		}
	}
}

func (a *adminAPI) checkpointTraffic() {
	a.flushPendingTraffic()
	if !a.dirty.Swap(false) {
		return
	}
	if err := a.saveTrafficStore(); err != nil {
		a.logger.Error("dashboard: save data: ", err)
		a.dirty.Store(true)
	}
}

func (a *adminAPI) renewExpiredAccounts(now int64) error {
	a.mutation.Lock()
	defer a.unlockMutation()
	a.flushPendingTrafficLocked()

	a.storeAccess.Lock()
	accountIDs := make(map[string]bool)
	previousAccounts := cloneAdminAccounts(a.store.Accounts)
	for identifier, account := range a.store.Accounts {
		if account == nil || account.PolicyScope != adminAccountPolicyGlobal || account.ResetDays <= 0 || account.ExpiresAt <= 0 || account.ExpiresAt > now {
			continue
		}
		period := int64(account.ResetDays) * adminDayMilliseconds
		steps := (now-account.ExpiresAt)/period + 1
		if steps > (math.MaxInt64-account.ExpiresAt)/period {
			a.store.Accounts = previousAccounts
			a.storeAccess.Unlock()
			return E.New("account renewal expiry overflow: ", identifier)
		}
		account.ExpiresAt += steps * period
		account.Enabled = true
		account.BaseUploadBytes = 0
		account.BaseDownloadBytes = 0
		account.Revision = nextAdminRevision(account.Revision, now)
		account.UpdatedAt = now
		accountIDs[identifier] = true
	}
	if len(accountIDs) == 0 {
		a.storeAccess.Unlock()
		return nil
	}

	previous := make(map[string]*adminInboundStore)
	baselines := make(map[string][]string)
	for tag, record := range a.store.Inbounds {
		updated := cloneInboundStore(record)
		changed := false
		for _, user := range updated.Users {
			if !accountIDs[user.AccountID] {
				continue
			}
			user.UploadBytes = 0
			user.DownloadBytes = 0
			user.TrafficGeneration++
			user.UpdatedAt = now
			baselines[tag] = append(baselines[tag], user.Name)
			changed = true
		}
		if changed {
			previous[tag] = cloneInboundStore(record)
			updated.Revision = nextAdminRevision(updated.Revision, now)
			a.store.Inbounds[tag] = updated
		}
	}
	tags := make([]string, 0, len(previous))
	for tag := range previous {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	a.storeAccess.Unlock()

	if err := a.commitInboundBatch(tags, previous); err != nil {
		a.storeAccess.Lock()
		a.store.Accounts = previousAccounts
		a.storeAccess.Unlock()
		var restoreErr error
		for _, tag := range tags {
			restoreErr = errors.Join(restoreErr, a.applyInbound(tag, true))
		}
		restoreErr = errors.Join(restoreErr, a.saveStore())
		return errors.Join(err, restoreErr)
	}
	a.trafficAccess.Lock()
	for tag, names := range baselines {
		for _, name := range names {
			a.baselineUserTrafficLocked(tag, name, false)
		}
	}
	a.trafficAccess.Unlock()
	var applyErr error
	for _, tag := range tags {
		applyErr = errors.Join(applyErr, a.applyInbound(tag, true))
	}
	return applyErr
}

func (a *adminAPI) reconcile() error {
	now := time.Now().UnixMilli()
	usage := a.activeUsage()
	a.storeAccess.RLock()
	accountUsage := a.accountUsageLocked(usage)
	a.storeAccess.RUnlock()
	if err := a.applyAllWithUsage(false, now, usage, accountUsage); err != nil {
		return err
	}
	if a.traffic == nil {
		return nil
	}
	connections := a.traffic.Connections()
	a.trafficAccess.Lock()
	baselines := make(map[uuid.UUID]adminTrafficBaseline, len(connections))
	for _, metadata := range connections {
		baselines[metadata.ID] = a.trafficBaselines[metadata.ID]
	}
	a.trafficAccess.Unlock()
	toClose := make([]uuid.UUID, 0)
	a.storeAccess.RLock()
	for _, metadata := range connections {
		if metadata.Metadata.User == "" {
			continue
		}
		sourceIP := ""
		if metadata.Metadata.Source.Addr.IsValid() {
			sourceIP = metadata.Metadata.Source.Addr.String()
		}
		if !a.userConnectionAllowedWithAccountsLocked(metadata.Metadata.Inbound, metadata.Metadata.User, baselines[metadata.ID].UserID, sourceIP, now, usage, accountUsage) {
			toClose = append(toClose, metadata.ID)
		}
	}
	a.storeAccess.RUnlock()
	for _, identifier := range toClose {
		if connection := a.traffic.Connection(identifier); connection != nil {
			_ = connection.Close()
		}
	}
	return nil
}

func (a *adminAPI) userConnectionAllowedLocked(inboundTag string, userName string, userID string, sourceIP string, now int64, usage map[string]adminUsage) bool {
	return a.userConnectionAllowedWithAccountsLocked(inboundTag, userName, userID, sourceIP, now, usage, a.accountUsageLocked(usage))
}

func (a *adminAPI) userConnectionAllowedWithAccountsLocked(inboundTag string, userName string, userID string, sourceIP string, now int64, usage map[string]adminUsage, accountUsage map[string]adminUsage) bool {
	record := a.store.Inbounds[inboundTag]
	if record == nil || !record.Authoritative {
		return true
	}
	if userID != "" {
		for _, user := range record.Users {
			if user.ID == userID {
				return a.adminUserConnectionAllowedLocked(user, sourceIP, now, usage[adminUserKey(inboundTag, user.Name)], accountUsage[user.AccountID])
			}
		}
		return false
	}
	return a.userAllowedLocked(inboundTag, userName, sourceIP, now, usage, accountUsage)
}

func (a *adminAPI) userAllowedLocked(inboundTag string, userName string, sourceIP string, now int64, usage map[string]adminUsage, accountUsage map[string]adminUsage) bool {
	record := a.store.Inbounds[inboundTag]
	if record == nil || !record.Authoritative {
		return true
	}
	for _, user := range record.Users {
		if user.Name == userName {
			return a.adminUserConnectionAllowedLocked(user, sourceIP, now, usage[adminUserKey(inboundTag, userName)], accountUsage[user.AccountID])
		}
	}
	return false
}

func (a *adminAPI) adminUserConnectionAllowedLocked(user *adminUser, sourceIP string, now int64, membershipUsage adminUsage, accountUsage adminUsage) bool {
	account := a.store.Accounts[user.AccountID]
	usage := membershipUsage
	maxIPs := user.MaxIPs
	if account != nil && account.PolicyScope == adminAccountPolicyGlobal {
		usage = accountUsage
		maxIPs = account.MaxIPs
	}
	if !adminUserEnabledWithAccount(user, account, now, membershipUsage, accountUsage) {
		return false
	}
	return adminSourceIPAllowed(maxIPs, sourceIP, usage.SourceSince)
}

func adminUserConnectionAllowed(user *adminUser, sourceIP string, now int64, usage adminUsage) bool {
	if !adminUserEnabled(user, now, usage) {
		return false
	}
	return adminSourceIPAllowed(user.MaxIPs, sourceIP, usage.SourceSince)
}

func adminSourceIPAllowed(maxIPs int, sourceIP string, sourceSince map[string]int64) bool {
	if maxIPs <= 0 || len(sourceSince) <= maxIPs {
		return true
	}
	connectedAt, exists := sourceSince[sourceIP]
	if !exists {
		return false
	}
	rank := 0
	for otherIP, otherConnectedAt := range sourceSince {
		if otherConnectedAt < connectedAt || otherConnectedAt == connectedAt && otherIP < sourceIP {
			rank++
		}
	}
	return rank < maxIPs
}

func adminUserEnabled(user *adminUser, now int64, active adminUsage) bool {
	if !user.Enabled || user.ExpiresAt > 0 && user.ExpiresAt <= now {
		return false
	}
	used := saturatingAdminTrafficAdd(user.UploadBytes, user.DownloadBytes, active.Upload, active.Download)
	return user.QuotaBytes <= 0 || used < user.QuotaBytes
}

func adminUserEnabledWithAccount(user *adminUser, account *adminAccount, now int64, membershipUsage adminUsage, accountUsage adminUsage) bool {
	if account == nil || account.PolicyScope != adminAccountPolicyGlobal {
		return adminUserEnabled(user, now, membershipUsage)
	}
	if !user.Enabled || !account.Enabled || account.ExpiresAt > 0 && account.ExpiresAt <= now {
		return false
	}
	used := saturatingAdminTrafficAdd(accountUsage.Upload, accountUsage.Download)
	return account.QuotaBytes <= 0 || used < account.QuotaBytes
}

func (a *adminAPI) accountUsageLocked(active map[string]adminUsage) map[string]adminUsage {
	return a.aggregateAccountUsageLocked(active, false)
}

func (a *adminAPI) allAccountUsageLocked(active map[string]adminUsage) map[string]adminUsage {
	return a.aggregateAccountUsageLocked(active, true)
}

func (a *adminAPI) aggregateAccountUsageLocked(active map[string]adminUsage, includeMembership bool) map[string]adminUsage {
	var result map[string]adminUsage
	if includeMembership {
		result = make(map[string]adminUsage, len(a.store.Accounts))
	}
	for identifier, account := range a.store.Accounts {
		if account == nil || !includeMembership && account.PolicyScope != adminAccountPolicyGlobal {
			continue
		}
		usage := adminUsage{}
		if account.PolicyScope == adminAccountPolicyGlobal {
			usage.Upload = account.BaseUploadBytes
			usage.Download = account.BaseDownloadBytes
		}
		if result == nil {
			result = make(map[string]adminUsage)
		}
		result[identifier] = usage
	}
	if len(result) == 0 {
		return result
	}
	for tag, inbound := range a.store.Inbounds {
		if inbound == nil {
			continue
		}
		for _, user := range inbound.Users {
			if user == nil {
				continue
			}
			usage, exists := result[user.AccountID]
			if !exists {
				continue
			}
			membershipUsage := active[adminUserKey(tag, user.Name)]
			usage.Upload = saturatingAdminTrafficAdd(usage.Upload, user.UploadBytes, membershipUsage.Upload)
			usage.Download = saturatingAdminTrafficAdd(usage.Download, user.DownloadBytes, membershipUsage.Download)
			usage.Connections += membershipUsage.Connections
			for address, since := range membershipUsage.SourceSince {
				if usage.SourceSince == nil {
					usage.SourceSince = make(map[string]int64)
				}
				if previous, found := usage.SourceSince[address]; !found || since < previous {
					usage.SourceSince[address] = since
				}
			}
			result[user.AccountID] = usage
		}
	}
	return result
}

func saturatingAdminTrafficAdd(values ...int64) int64 {
	var result int64
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if value > math.MaxInt64-result {
			return math.MaxInt64
		}
		result += value
	}
	return result
}

func (a *adminAPI) applyAll(force bool) error {
	active := a.activeUsage()
	now := time.Now().UnixMilli()
	a.storeAccess.RLock()
	accountUsage := a.accountUsageLocked(active)
	a.storeAccess.RUnlock()
	return a.applyAllWithUsage(force, now, active, accountUsage)
}

func (a *adminAPI) applyAllWithUsage(force bool, now int64, active map[string]adminUsage, accountUsage map[string]adminUsage) error {
	for tag, runtimeInbound := range a.runtimes {
		if runtimeInbound.Manager == nil {
			continue
		}
		if err := a.applyInboundWithUsage(tag, force, now, active, accountUsage); err != nil {
			return E.Cause(err, "update inbound ", tag)
		}
	}
	return nil
}

func (a *adminAPI) applyInbound(tag string, force bool) error {
	active := a.activeUsage()
	now := time.Now().UnixMilli()
	a.storeAccess.RLock()
	accountUsage := a.accountUsageLocked(active)
	a.storeAccess.RUnlock()
	return a.applyInboundWithUsage(tag, force, now, active, accountUsage)
}

func (a *adminAPI) applyInboundWithUsage(tag string, force bool, now int64, active map[string]adminUsage, accountUsage map[string]adminUsage) error {
	runtimeInbound := a.runtimes[tag]
	if runtimeInbound == nil || runtimeInbound.Manager == nil {
		return os.ErrNotExist
	}
	manager := runtimeInbound.Manager
	manager.applyAccess.Lock()
	defer manager.applyAccess.Unlock()
	a.storeAccess.RLock()
	record := a.store.Inbounds[tag]
	if record == nil {
		a.storeAccess.RUnlock()
		return os.ErrNotExist
	}
	revision := record.Revision
	users := make([]adapter.ManagedUser, 0, len(record.Users))
	for _, user := range record.Users {
		membershipUsage := active[adminUserKey(tag, user.Name)]
		if adminUserEnabledWithAccount(user, a.store.Accounts[user.AccountID], now, membershipUsage, accountUsage[user.AccountID]) {
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
	if !force && manager.lastSignature == signature {
		a.markInboundApplied(tag, revision)
		return nil
	}
	if err := manager.service.UpdateManagedUsers(users); err != nil {
		return err
	}
	manager.lastSignature = signature
	a.markInboundApplied(tag, revision)
	return nil
}

func (a *adminAPI) markInboundApplied(tag string, revision int64) {
	a.storeAccess.Lock()
	if record := a.store.Inbounds[tag]; record != nil && record.Revision == revision {
		record.AppliedRevision = revision
	}
	a.storeAccess.Unlock()
}

func (a *adminAPI) activeUsage() map[string]adminUsage {
	result := make(map[string]adminUsage)
	if a.traffic == nil {
		return result
	}
	connections := a.traffic.Connections()
	var identities map[string]adminManagedUserIdentity
	var names map[string]string
	if len(connections) > 16 {
		identities = make(map[string]adminManagedUserIdentity)
		names = make(map[string]string)
		a.storeAccess.RLock()
		for tag, inbound := range a.store.Inbounds {
			if inbound == nil {
				continue
			}
			for _, user := range inbound.Users {
				if user == nil {
					continue
				}
				identities[adminUserKey(tag, user.Name)] = adminManagedUserIdentity{ID: user.ID, Generation: user.TrafficGeneration}
				names[adminUserKey(tag, user.ID)] = user.Name
			}
		}
		a.storeAccess.RUnlock()
	}
	a.trafficAccess.Lock()
	defer a.trafficAccess.Unlock()
	for _, metadata := range connections {
		if metadata.Metadata.User == "" {
			continue
		}
		baseline := a.trafficBaselines[metadata.ID]
		if baseline.UserID == "" {
			if identities != nil {
				identity := identities[adminUserKey(metadata.Metadata.Inbound, metadata.Metadata.User)]
				baseline.UserID, baseline.Generation = identity.ID, identity.Generation
			} else {
				baseline.UserID, baseline.Generation = a.managedUserIdentity(metadata.Metadata.Inbound, metadata.Metadata.User)
			}
			a.trafficBaselines[metadata.ID] = baseline
		}
		accountingName := metadata.Metadata.User
		if baseline.UserID != "" {
			currentName := ""
			if names != nil {
				currentName = names[adminUserKey(metadata.Metadata.Inbound, baseline.UserID)]
			} else {
				currentName = a.managedUserName(metadata.Metadata.Inbound, baseline.UserID)
			}
			if currentName == "" {
				continue
			}
			accountingName = currentName
		}
		key := adminUserKey(metadata.Metadata.Inbound, accountingName)
		usage := result[key]
		usage.Upload = saturatingAdminTrafficAdd(usage.Upload, max(metadata.Upload.Load()-baseline.Upload, 0))
		usage.Download = saturatingAdminTrafficAdd(usage.Download, max(metadata.Download.Load()-baseline.Download, 0))
		usage.Connections++
		sourceIP := ""
		if metadata.Metadata.Source.Addr.IsValid() {
			sourceIP = metadata.Metadata.Source.Addr.String()
		}
		if sourceIP != "" {
			if usage.SourceSince == nil {
				usage.SourceSince = make(map[string]int64)
			}
			createdAt := metadata.CreatedAt.UnixMilli()
			if previous, exists := usage.SourceSince[sourceIP]; !exists || createdAt < previous {
				usage.SourceSince[sourceIP] = createdAt
			}
		}
		result[key] = usage
	}
	return result
}

func (a *adminAPI) managedUserName(tag string, userID string) string {
	a.storeAccess.RLock()
	defer a.storeAccess.RUnlock()
	record := a.store.Inbounds[tag]
	if record == nil {
		return ""
	}
	for _, user := range record.Users {
		if user.ID == userID {
			return user.Name
		}
	}
	return ""
}

// baselineUserTrafficLocked resets active-connection accounting for a user.
// When settle is true, bytes before the new baseline are first persisted.
// trafficAccess must be held by the caller.
func (a *adminAPI) baselineUserTrafficLocked(tag string, userName string, settle bool) {
	if a.traffic == nil || userName == "" {
		return
	}
	userID, generation := a.managedUserIdentity(tag, userName)
	a.baselineUserTrafficForIdentityLocked(tag, userName, userID, generation, settle)
}

func (a *adminAPI) baselineUserTrafficForIdentityLocked(tag string, userName string, userID string, generation int64, settle bool) {
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
		a.trafficBaselines[metadata.ID] = adminTrafficBaseline{
			Upload: currentUpload, Download: currentDownload, UserID: userID, Generation: generation,
		}
	}
	if !settle || settledUpload == 0 && settledDownload == 0 {
		return
	}
	a.storeAccess.Lock()
	if record := a.store.Inbounds[tag]; record != nil {
		for _, user := range record.Users {
			if user.Name == userName {
				user.UploadBytes = saturatingAdminTrafficAdd(user.UploadBytes, settledUpload)
				user.DownloadBytes = saturatingAdminTrafficAdd(user.DownloadBytes, settledDownload)
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
	defer a.unlockMutation()
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
		if baseline.UserID == "" {
			for _, user := range record.Users {
				if user.Name == userName {
					baseline.UserID = user.ID
					baseline.Generation = user.TrafficGeneration
					break
				}
			}
		}
		for _, user := range record.Users {
			matches := (baseline.UserID != "" && user.ID == baseline.UserID) || (baseline.UserID == "" && user.Name == userName)
			if matches && user.TrafficGeneration == baseline.Generation {
				user.UploadBytes = saturatingAdminTrafficAdd(user.UploadBytes, upload)
				user.DownloadBytes = saturatingAdminTrafficAdd(user.DownloadBytes, download)
				user.UpdatedAt = time.Now().UnixMilli()
				break
			}
		}
		a.trafficBaselines[metadata.ID] = adminTrafficBaseline{
			Upload: metadata.Upload.Load(), Download: metadata.Download.Load(),
			UserID: baseline.UserID, Generation: baseline.Generation,
		}
	}
}

func adminUserKey(inbound string, name string) string {
	return inbound + "\x00" + name
}

func (a *adminAPI) buildRouter() http.Handler {
	router := chi.NewRouter()
	router.Group(func(router chi.Router) {
		router.Use(a.authenticate)
		router.Get(adminRoutePrefix+"/settings", a.getSettings)
		router.Put(adminRoutePrefix+"/settings", a.updateSettings)
		a.register3XUIImportRoutes(router)
		router.Get(adminRoutePrefix+"/overview", a.getOverview)
		router.Get(adminRoutePrefix+"/protocols", a.listProtocols)
		router.Get(adminRoutePrefix+"/servers", a.listServers)
		router.Get(adminRoutePrefix+"/servers/{tag}", a.getServer)
		router.Post(adminRoutePrefix+"/servers", a.createServer)
		router.Put(adminRoutePrefix+"/servers/{tag}", a.updateServer)
		router.Delete(adminRoutePrefix+"/servers/{tag}", a.deleteServer)
		router.Post(adminRoutePrefix+"/reload", a.reloadCore)
		router.Get(adminRoutePrefix+"/users", a.listUsers)
		router.Get(adminRoutePrefix+"/user-groups", a.getUserGroup)
		router.Get(adminRoutePrefix+"/user-groups/{name}", a.getUserGroup)
		router.Post(adminRoutePrefix+"/user-groups", a.createUserGroup)
		router.Put(adminRoutePrefix+"/user-groups", a.updateUserGroup)
		router.Put(adminRoutePrefix+"/user-groups/{name}", a.updateUserGroup)
		router.Delete(adminRoutePrefix+"/user-groups", a.deleteUserGroup)
		router.Delete(adminRoutePrefix+"/user-groups/{name}", a.deleteUserGroup)
		router.Post(adminRoutePrefix+"/user-groups/reset-traffic", a.resetUserGroupTraffic)
		router.Post(adminRoutePrefix+"/user-groups/{name}/reset-traffic", a.resetUserGroupTraffic)
		router.Get(adminRoutePrefix+"/users/{id}", a.getUser)
		router.Post(adminRoutePrefix+"/users", a.createUser)
		router.Put(adminRoutePrefix+"/users/{id}", a.updateUser)
		router.Delete(adminRoutePrefix+"/users/{id}", a.deleteUser)
		router.Post(adminRoutePrefix+"/users/{id}/reset-traffic", a.resetUserTraffic)
		router.Get(adminRoutePrefix+"/connections", a.listConnections)
		router.Delete(adminRoutePrefix+"/connections", a.closeAllConnections)
		router.Delete(adminRoutePrefix+"/connections/{id}", a.closeConnection)
	})
	return router
}

func (a *adminAPI) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	a.handlerAccess.Lock()
	if a.closing {
		a.handlerAccess.Unlock()
		writeAdminError(writer, http.StatusServiceUnavailable, "管理服務正在關閉")
		return
	}
	a.handlers.Add(1)
	a.handlerAccess.Unlock()
	defer a.handlers.Done()
	writer.Header().Set("Cache-Control", "no-store")
	if a.servePublicRoute(writer, request) {
		return
	}
	a.router.ServeHTTP(writer, request)
}

func (a *adminAPI) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !sameOriginAdminRequest(request, a.secure) {
			writeAdminError(writer, http.StatusForbidden, "管理 API 只允許同源瀏覽器請求")
			return
		}
		if a.secret == "" {
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

func sameOriginAdminRequest(request *http.Request, secure bool) bool {
	if request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Host == "" || parsedOrigin.Scheme == "" {
		return false
	}
	expectedScheme := "http"
	if secure || request.TLS != nil {
		expectedScheme = "https"
	} else if forwardedScheme := trustedForwardedScheme(request); forwardedScheme != "" {
		expectedScheme = forwardedScheme
	}
	return strings.EqualFold(parsedOrigin.Scheme, expectedScheme) && strings.EqualFold(parsedOrigin.Host, request.Host)
}

func trustedForwardedScheme(request *http.Request) string {
	if !requestFromLoopback(request) {
		return ""
	}
	forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0])
	if forwarded == "http" || forwarded == "https" {
		return forwarded
	}
	return ""
}

func requestFromLoopback(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	address := net.ParseIP(strings.Trim(host, "[]"))
	return address != nil && address.IsLoopback()
}

func (a *adminAPI) getOverview(writer http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	a.overviewAccess.Lock()
	content := a.overviewContent
	if len(content) == 0 || !now.Before(a.overviewExpires) {
		var err error
		content, err = json.Marshal(a.buildOverview(now))
		if err != nil {
			a.overviewAccess.Unlock()
			writeAdminError(writer, http.StatusInternalServerError, "無法建立 Core 狀態")
			return
		}
		content = append(content, '\n')
		a.overviewContent = content
		a.overviewExpires = now.Add(adminOverviewCacheDuration)
	}
	a.overviewAccess.Unlock()
	writeAdminJSONContent(writer, http.StatusOK, content)
}

func (a *adminAPI) buildOverview(now time.Time) map[string]any {
	nowMilliseconds := now.UnixMilli()
	active := a.activeUsage()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	var totalUsers, enabledUsers, disabledUsers, expiredUsers int
	a.storeAccess.RLock()
	accountUsage := a.accountUsageLocked(active)
	summaries := a.inboundSummariesLocked(nowMilliseconds, active, accountUsage)
	for tag, record := range a.store.Inbounds {
		if a.runtimes[tag] == nil || a.runtimes[tag].Manager == nil {
			continue
		}
		for _, user := range record.Users {
			totalUsers++
			account := a.store.Accounts[user.AccountID]
			if !user.Enabled || account != nil && account.PolicyScope == adminAccountPolicyGlobal && !account.Enabled {
				disabledUsers++
			}
			expiresAt := user.ExpiresAt
			if account != nil && account.PolicyScope == adminAccountPolicyGlobal {
				expiresAt = account.ExpiresAt
			}
			if expiresAt > 0 && expiresAt <= nowMilliseconds {
				expiredUsers++
			}
			membershipUsage := active[adminUserKey(tag, user.Name)]
			if adminUserEnabledWithAccount(user, account, nowMilliseconds, membershipUsage, accountUsage[user.AccountID]) {
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
	return map[string]any{
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
		"features": map[string]bool{
			"three_x_ui_import": admin3XUIImportAvailable,
		},
	}
}

func (a *adminAPI) inboundSummariesLocked(now int64, active map[string]adminUsage, accountUsage map[string]adminUsage) []adminInboundSummary {
	summaries := make([]adminInboundSummary, 0, len(a.inbounds))
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
				summary.Revision = record.Revision
				summary.AppliedRevision = record.AppliedRevision
				summary.Pending = record.Revision != record.AppliedRevision
				for _, user := range record.Users {
					membershipUsage := active[adminUserKey(runtimeInbound.Tag, user.Name)]
					if adminUserEnabledWithAccount(user, a.store.Accounts[user.AccountID], now, membershipUsage, accountUsage[user.AccountID]) {
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
	now := time.Now().UnixMilli()
	views := make([]adminUserView, 0)
	a.storeAccess.RLock()
	accountUsage := a.allAccountUsageLocked(active)
	for tag, record := range a.store.Inbounds {
		if filterInbound != "" && tag != filterInbound {
			continue
		}
		if runtimeInbound := a.runtimes[tag]; runtimeInbound == nil || runtimeInbound.Manager == nil {
			continue
		}
		for _, user := range record.Users {
			usage := active[adminUserKey(tag, user.Name)]
			views = append(views, makeAdminUserView(user, record, usage, false))
		}
	}
	accounts := a.accountViewsLocked(accountUsage)
	summaries := a.inboundSummariesLocked(now, active, accountUsage)
	a.storeAccess.RUnlock()
	sort.Slice(views, func(i, j int) bool {
		if views[i].Name == views[j].Name {
			return views[i].Inbound < views[j].Inbound
		}
		return strings.ToLower(views[i].Name) < strings.ToLower(views[j].Name)
	})
	writeAdminJSON(writer, http.StatusOK, map[string]any{
		"users":    views,
		"accounts": accounts,
		"inbounds": summaries,
	})
}

func (a *adminAPI) accountViews(active map[string]adminUsage, accountUsage map[string]adminUsage) []adminAccountView {
	a.storeAccess.RLock()
	defer a.storeAccess.RUnlock()
	if len(accountUsage) < len(a.store.Accounts) {
		accountUsage = a.allAccountUsageLocked(active)
	}
	return a.accountViewsLocked(accountUsage)
}

func (a *adminAPI) accountViewsLocked(accountUsage map[string]adminUsage) []adminAccountView {
	views := make([]adminAccountView, 0, len(a.store.Accounts))
	for _, account := range a.store.Accounts {
		if account == nil {
			continue
		}
		usage := accountUsage[account.ID]
		views = append(views, makeAdminAccountView(account, usage))
	}
	sort.Slice(views, func(left, right int) bool {
		return strings.ToLower(views[left].Name) < strings.ToLower(views[right].Name)
	})
	return views
}

func makeAdminAccountView(account *adminAccount, usage adminUsage) adminAccountView {
	view := adminAccountView{adminAccount: *account, UploadBytes: usage.Upload, DownloadBytes: usage.Download, ActiveConnections: usage.Connections}
	if len(usage.SourceSince) > 0 {
		view.OnlineIPs = make([]adminOnlineIP, 0, len(usage.SourceSince))
		for address, since := range usage.SourceSince {
			view.OnlineIPs = append(view.OnlineIPs, adminOnlineIP{Address: address, Since: since})
		}
		sort.Slice(view.OnlineIPs, func(left, right int) bool { return view.OnlineIPs[left].Address < view.OnlineIPs[right].Address })
	}
	return view
}

func (a *adminAPI) getUser(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	active := a.activeUsage()
	a.storeAccess.RLock()
	defer a.storeAccess.RUnlock()
	for tag, record := range a.store.Inbounds {
		if runtimeInbound := a.runtimes[tag]; runtimeInbound == nil || runtimeInbound.Manager == nil {
			continue
		}
		for _, user := range record.Users {
			if user.ID == id {
				view := makeAdminUserView(user, record, active[adminUserKey(tag, user.Name)], true)
				if externalID := a.store.ExternalSubscriptions[user.Name]; validExternalSubscriptionID(externalID) && a.publicBaseURL != "" {
					view.SubscriptionURL = a.publicBaseURL + "/sub/" + url.PathEscape(externalID)
				} else if token := a.store.Subscriptions[user.Name]; token != "" && a.publicBaseURL != "" && len(a.subscriptionLinksLocked(user.Name, time.Now().UnixMilli(), active)) > 0 {
					view.SubscriptionURL = a.publicBaseURL + a.subscriptionPathLocked() + token
				}
				writeAdminJSON(writer, http.StatusOK, view)
				return
			}
		}
	}
	writeAdminError(writer, http.StatusNotFound, "找不到用戶")
}

func makeAdminUserView(user *adminUser, record *adminInboundStore, usage adminUsage, includeCredentials bool) adminUserView {
	copyUser := *user
	if !includeCredentials {
		copyUser.UUID = ""
		copyUser.Password = ""
	}
	copyUser.UploadBytes = saturatingAdminTrafficAdd(copyUser.UploadBytes, usage.Upload)
	copyUser.DownloadBytes = saturatingAdminTrafficAdd(copyUser.DownloadBytes, usage.Download)
	var onlineIPs []adminOnlineIP
	if len(usage.SourceSince) > 0 {
		onlineIPs = make([]adminOnlineIP, 0, len(usage.SourceSince))
		for address, since := range usage.SourceSince {
			onlineIPs = append(onlineIPs, adminOnlineIP{Address: address, Since: since})
		}
		sort.Slice(onlineIPs, func(i, j int) bool { return onlineIPs[i].Address < onlineIPs[j].Address })
	}
	return adminUserView{
		adminUser: copyUser, ActiveConnections: usage.Connections,
		OnlineIPs: onlineIPs,
		Revision:  record.Revision, AppliedRevision: record.AppliedRevision,
	}
}

func (a *adminAPI) createUser(writer http.ResponseWriter, request *http.Request) {
	var input adminUserInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	a.mutation.Lock()
	defer a.unlockMutation()
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
	if normalized.MaxIPs != nil {
		newUser.MaxIPs = *normalized.MaxIPs
	}
	previous, err := a.mutateInbound(input.Inbound, normalized.Revision, true, func(record *adminInboundStore) error {
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
	a.storeAccess.RLock()
	view := makeAdminUserView(newUser, a.store.Inbounds[input.Inbound], adminUsage{}, true)
	a.storeAccess.RUnlock()
	writeAdminJSON(writer, http.StatusCreated, view)
}

func (a *adminAPI) updateUser(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	var input adminUserInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	a.mutation.Lock()
	defer a.unlockMutation()
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
	if err = a.requireInboundRevision(tag, normalized.Revision); err != nil {
		writeAdminError(writer, http.StatusConflict, err.Error())
		return
	}
	candidate := *current
	candidate.Name = normalized.Name
	candidate.UUID = normalized.UUID
	candidate.Password = normalized.Password
	candidate.Flow = normalized.Flow
	candidate.AlterID = normalized.AlterID
	if normalized.MaxIPs != nil {
		candidate.MaxIPs = *normalized.MaxIPs
	}
	a.storeAccess.RLock()
	err = validateUniqueUser(a.store.Inbounds[tag], &candidate, id)
	a.storeAccess.RUnlock()
	if err != nil {
		writeAdminError(writer, http.StatusConflict, err.Error())
		return
	}
	if normalized.Name != current.Name {
		a.storeAccess.Lock()
		if a.userAliases == nil {
			a.userAliases = make(map[string]adminManagedUserIdentity)
		}
		a.userAliases[adminUserKey(tag, current.Name)] = adminManagedUserIdentity{
			ID: current.ID, Generation: current.TrafficGeneration,
		}
		a.storeAccess.Unlock()
		a.trafficAccess.Lock()
		a.baselineUserTrafficLocked(tag, current.Name, true)
		a.trafficAccess.Unlock()
	}
	var updated adminUser
	previous, err := a.mutateInbound(tag, normalized.Revision, true, func(record *adminInboundStore) error {
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
				if normalized.MaxIPs != nil {
					updated.MaxIPs = *normalized.MaxIPs
				}
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
	a.storeAccess.RLock()
	view := makeAdminUserView(&updated, a.store.Inbounds[tag], adminUsage{}, true)
	a.storeAccess.RUnlock()
	writeAdminJSON(writer, http.StatusOK, view)
}

func (a *adminAPI) deleteUser(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	expectedRevision, err := requestedAdminRevision(request)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, err.Error())
		return
	}
	a.mutation.Lock()
	defer a.unlockMutation()
	tag, current := a.findUser(id)
	if current == nil {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	if err = a.requireInboundRevision(tag, expectedRevision); err != nil {
		writeAdminError(writer, http.StatusConflict, err.Error())
		return
	}
	previous, err := a.mutateInbound(tag, expectedRevision, true, func(record *adminInboundStore) error {
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
		if errors.Is(err, os.ErrNotExist) {
			writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		} else {
			writeAdminError(writer, http.StatusConflict, err.Error())
		}
		return
	}
	if err = a.commitMutation(tag, previous); err != nil {
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	a.trafficAccess.Lock()
	a.baselineUserTrafficForIdentityLocked(tag, current.Name, current.ID, current.TrafficGeneration, false)
	a.trafficAccess.Unlock()
	writer.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) resetUserTraffic(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	a.mutation.Lock()
	defer a.unlockMutation()
	tag, current := a.findUser(id)
	if current == nil {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	previous, err := a.mutateInbound(tag, 0, false, func(record *adminInboundStore) error {
		for _, user := range record.Users {
			if user.ID == id {
				user.UploadBytes = 0
				user.DownloadBytes = 0
				user.TrafficGeneration++
				user.UpdatedAt = time.Now().UnixMilli()
				return nil
			}
		}
		return os.ErrNotExist
	})
	if err != nil {
		writeAdminError(writer, http.StatusNotFound, "找不到用戶")
		return
	}
	if err = a.saveStore(); err != nil {
		a.storeAccess.Lock()
		a.store.Inbounds[tag] = previous
		a.storeAccess.Unlock()
		restoreErr := a.saveStore()
		writeAdminError(writer, http.StatusInternalServerError, errors.Join(E.Cause(err, "儲存流量資料失敗"), restoreErr).Error())
		return
	}
	a.trafficAccess.Lock()
	a.baselineUserTrafficLocked(tag, current.Name, false)
	a.trafficAccess.Unlock()
	writer.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) mutateInbound(tag string, expectedRevision int64, advanceRevision bool, mutate func(record *adminInboundStore) error) (*adminInboundStore, error) {
	a.storeAccess.Lock()
	defer a.storeAccess.Unlock()
	record := a.store.Inbounds[tag]
	if record == nil {
		return nil, os.ErrNotExist
	}
	if advanceRevision {
		if expectedRevision <= 0 {
			return nil, E.New("缺少資料 revision，請重新整理後再試")
		}
		if record.Revision != expectedRevision {
			return nil, E.New("資料已被其他操作更新，請重新整理後再試")
		}
	}
	previous := cloneInboundStore(record)
	updated := cloneInboundStore(record)
	if err := mutate(updated); err != nil {
		return nil, err
	}
	if advanceRevision {
		updated.Revision = nextAdminRevision(record.Revision, time.Now().UnixMilli())
	}
	a.store.Inbounds[tag] = updated
	return previous, nil
}

func (a *adminAPI) requireInboundRevision(tag string, expectedRevision int64) error {
	a.storeAccess.RLock()
	defer a.storeAccess.RUnlock()
	record := a.store.Inbounds[tag]
	if record == nil {
		return os.ErrNotExist
	}
	if expectedRevision <= 0 {
		return E.New("缺少資料 revision，請重新整理後再試")
	}
	if record.Revision != expectedRevision {
		return E.New("資料已被其他操作更新，請重新整理後再試")
	}
	return nil
}

func requestedAdminRevision(request *http.Request) (int64, error) {
	value := request.URL.Query().Get("revision")
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision <= 0 {
		return 0, E.New("revision 格式不正確")
	}
	return revision, nil
}

func (a *adminAPI) commitMutation(tag string, previous *adminInboundStore) error {
	a.storeAccess.RLock()
	previousAccounts := cloneAdminAccounts(a.store.Accounts)
	previousSubscriptions := maps.Clone(a.store.Subscriptions)
	previousExternalSubscriptions := maps.Clone(a.store.ExternalSubscriptions)
	a.storeAccess.RUnlock()
	if err := a.applyInbound(tag, true); err != nil {
		rollbackErr := a.rollbackMutation(tag, previous, previousAccounts, previousSubscriptions, previousExternalSubscriptions, false)
		return errors.Join(E.Cause(err, "更新核心用戶失敗"), rollbackErr)
	}
	if err := a.saveStore(); err != nil {
		rollbackErr := a.rollbackMutation(tag, previous, previousAccounts, previousSubscriptions, previousExternalSubscriptions, true)
		return errors.Join(E.Cause(err, "儲存用戶資料失敗"), rollbackErr)
	}
	// Persistence can reassign accounts after a legacy single-user rename.
	if err := a.applyInbound(tag, true); err != nil {
		rollbackErr := a.rollbackMutation(tag, previous, previousAccounts, previousSubscriptions, previousExternalSubscriptions, true)
		return errors.Join(E.Cause(err, "更新核心帳戶政策失敗"), rollbackErr)
	}
	return nil
}

func (a *adminAPI) rollbackMutation(tag string, previous *adminInboundStore, accounts map[string]*adminAccount, subscriptions map[string]string, externalSubscriptions map[string]string, persist bool) error {
	a.storeAccess.Lock()
	a.store.Inbounds[tag] = previous
	a.store.Accounts = accounts
	a.store.Subscriptions = subscriptions
	a.store.ExternalSubscriptions = externalSubscriptions
	a.storeAccess.Unlock()
	applyErr := a.applyInbound(tag, true)
	var saveErr error
	if persist {
		saveErr = a.saveStore()
	}
	return errors.Join(applyErr, saveErr)
}

func cloneInboundStore(record *adminInboundStore) *adminInboundStore {
	if record == nil {
		return nil
	}
	copyRecord := *record
	copyRecord.Users = make([]*adminUser, len(record.Users))
	for index, user := range record.Users {
		copyUser := *user
		copyRecord.Users[index] = &copyUser
	}
	return &copyRecord
}

func cloneAdminInboundStores(inbounds map[string]*adminInboundStore) map[string]*adminInboundStore {
	cloned := make(map[string]*adminInboundStore, len(inbounds))
	for tag, record := range inbounds {
		cloned[tag] = cloneInboundStore(record)
	}
	return cloned
}

func cloneAdminServerStores(servers map[string]*adminServerStore) map[string]*adminServerStore {
	cloned := make(map[string]*adminServerStore, len(servers))
	for tag, profile := range servers {
		cloned[tag] = cloneAdminServerStore(profile)
	}
	return cloned
}

func cloneAdminAccounts(accounts map[string]*adminAccount) map[string]*adminAccount {
	result := make(map[string]*adminAccount, len(accounts))
	for identifier, account := range accounts {
		if account == nil {
			result[identifier] = nil
			continue
		}
		copyAccount := *account
		result[identifier] = &copyAccount
	}
	return result
}

func cloneAdminStore(store adminStore) adminStore {
	return adminStore{
		Version:               store.Version,
		Accounts:              cloneAdminAccounts(store.Accounts),
		Inbounds:              cloneAdminInboundStores(store.Inbounds),
		Servers:               cloneAdminServerStores(store.Servers),
		Subscriptions:         maps.Clone(store.Subscriptions),
		ExternalSubscriptions: maps.Clone(store.ExternalSubscriptions),
		Settings:              store.Settings,
	}
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
	if input.QuotaBytes < 0 || input.ExpiresAt < 0 || input.AlterID < 0 || input.MaxIPs != nil && *input.MaxIPs < 0 {
		return input, E.New("額度、到期時間、Alter ID 與 IP 限制不可為負數")
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
	return a.saveStoreNormalized(true)
}

func (a *adminAPI) saveTrafficStore() error {
	return a.saveStoreNormalized(false)
}

func (a *adminAPI) saveStoreNormalized(normalize bool) error {
	if a.validationOnly {
		return nil
	}
	a.saveAccess.Lock()
	defer a.saveAccess.Unlock()
	a.storeAccess.Lock()
	if normalize {
		if err := ensureAdminAccounts(&a.store, time.Now().UnixMilli()); err != nil {
			a.storeAccess.Unlock()
			return err
		}
		if err := a.ensureSubscriptionTokensLocked(); err != nil {
			a.storeAccess.Unlock()
			return err
		}
		if err := a.scrubAuthoritativeProfileUsersLocked(); err != nil {
			a.storeAccess.Unlock()
			return err
		}
	}
	store := cloneAdminStore(a.store)
	a.storeAccess.Unlock()
	content, err := json.MarshalIndent(store, "", "  ")
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
	if err = filemanager.Remove(a.ctx, tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := filemanager.OpenFile(a.ctx, tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer filemanager.Remove(a.ctx, tempPath)
	content = append(content, '\n')
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if err = secureAdminStateFile(a.ctx, tempPath); err != nil {
		return err
	}

	_, statErr := filemanager.Stat(a.ctx, a.dataPath)
	hasCurrent := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if hasCurrent {
		if err = secureAdminStateFile(a.ctx, a.dataPath); err != nil {
			return err
		}
		if err = filemanager.Remove(a.ctx, backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = filemanager.Rename(a.ctx, a.dataPath, backupPath); err != nil {
			return err
		}
	}
	if err = replaceAdminStateFile(a.ctx, tempPath, a.dataPath); err != nil {
		if hasCurrent {
			err = errors.Join(err, replaceAdminStateFile(a.ctx, backupPath, a.dataPath))
		}
		return err
	}
	if err = syncAdminStateDirectory(a.ctx, parent); err != nil {
		return E.Cause(err, "sync dashboard data directory")
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

func writeAdminJSONContent(writer http.ResponseWriter, status int, content []byte) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(content)
}

func writeAdminError(writer http.ResponseWriter, status int, message string) {
	writeAdminJSON(writer, status, map[string]string{"error": message})
}
