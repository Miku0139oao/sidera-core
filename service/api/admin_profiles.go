package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	stdjson "encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
	SJSON "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"

	"github.com/go-chi/chi/v5"
)

const (
	adminServerKindInbound  = "inbound"
	adminServerKindEndpoint = "endpoint"
)

type adminProtocolSpec struct {
	Kind         string
	Type         string
	Category     string
	Network      string
	TLS          string
	Credential   string
	UpdatePolicy string
	Description  string
	Composite    bool
}

var adminProtocolSpecs = []adminProtocolSpec{
	{adminServerKindInbound, C.TypeSOCKS, "standard", "tcp+udp", "none", adapter.ManagedUserCredentialPassword, "hot", "SOCKS4/4a/5 proxy server.", false},
	{adminServerKindInbound, C.TypeHTTP, "standard", "tcp", "optional", adapter.ManagedUserCredentialPassword, "hot", "HTTP CONNECT proxy with optional TLS.", false},
	{adminServerKindInbound, C.TypeMixed, "standard", "tcp+udp", "optional", adapter.ManagedUserCredentialPassword, "hot", "Combined SOCKS and HTTP proxy listener.", false},
	{adminServerKindInbound, C.TypeShadowsocks, "encrypted", "tcp+udp", "none", adapter.ManagedUserCredentialPassword, "hot", "Shadowsocks, including multi-user 2022 methods.", false},
	{adminServerKindInbound, C.TypeSnell, "encrypted", "tcp", "none", adapter.ManagedUserCredentialPassword, "hot", "Snell version 5 or 6 server.", false},
	{adminServerKindInbound, C.TypeVMess, "v2ray", "tcp+udp", "optional", adapter.ManagedUserCredentialUUID, "hot", "VMess with TLS and V2Ray transports.", false},
	{adminServerKindInbound, C.TypeTrojan, "tls", "tcp+udp", "optional", adapter.ManagedUserCredentialPassword, "hot", "Trojan with TLS and V2Ray transports.", false},
	{adminServerKindInbound, C.TypeNaive, "tls", "tcp+udp", "required", adapter.ManagedUserCredentialPassword, "hot", "NaiveProxy over HTTP/2 or HTTP/3.", false},
	{adminServerKindInbound, C.TypeShadowTLS, "transport", "tcp", "none", adapter.ManagedUserCredentialPassword, "hot", "ShadowTLS transport; clients also need an inner proxy.", true},
	{adminServerKindInbound, C.TypeVLESS, "v2ray", "tcp+udp", "optional", adapter.ManagedUserCredentialUUID, "hot", "VLESS with Vision, Reality, TLS and V2Ray transports.", false},
	{adminServerKindInbound, C.TypeAnyTLS, "tls", "tcp+udp", "required", adapter.ManagedUserCredentialPassword, "hot", "AnyTLS multiplexed TLS proxy.", false},
	{adminServerKindInbound, C.TypeHysteria, "quic", "udp", "required", adapter.ManagedUserCredentialPassword, "disruptive", "Hysteria QUIC proxy.", false},
	{adminServerKindInbound, C.TypeTUIC, "quic", "udp", "required", adapter.ManagedUserCredentialUUIDPassword, "disruptive", "TUIC v5 QUIC proxy.", false},
	{adminServerKindInbound, C.TypeHysteria2, "quic", "udp", "required", adapter.ManagedUserCredentialPassword, "disruptive", "Hysteria 2 QUIC proxy.", false},
	{adminServerKindEndpoint, C.TypeOpenVPNServer, "vpn", "tcp+udp", "required", adapter.ManagedUserCredentialPassword, "disruptive", "OpenVPN server endpoint.", false},
}

type adminProtocolView struct {
	Kind         string             `json:"kind"`
	Type         string             `json:"type"`
	Name         string             `json:"name"`
	Category     string             `json:"category"`
	Network      string             `json:"network"`
	TLS          string             `json:"tls"`
	Credential   string             `json:"credential"`
	UpdatePolicy string             `json:"update_policy"`
	Description  string             `json:"description"`
	Composite    bool               `json:"composite,omitempty"`
	Template     stdjson.RawMessage `json:"template"`
}

type adminServerInput struct {
	Kind      string               `json:"kind"`
	Config    stdjson.RawMessage   `json:"config"`
	Advertise adminServerAdvertise `json:"advertise"`
}

type adminServerView struct {
	Tag             string               `json:"tag"`
	Type            string               `json:"type"`
	Kind            string               `json:"kind"`
	Source          string               `json:"source"`
	Status          string               `json:"status"`
	Editable        bool                 `json:"editable"`
	Pending         bool                 `json:"pending"`
	Managed         bool                 `json:"managed"`
	Credential      string               `json:"credential,omitempty"`
	UpdatePolicy    string               `json:"update_policy,omitempty"`
	Advertise       adminServerAdvertise `json:"advertise,omitempty"`
	Config          stdjson.RawMessage   `json:"config,omitempty"`
	Revision        int64                `json:"revision,omitempty"`
	AppliedRevision int64                `json:"applied_revision,omitempty"`
}

type decodedAdminServer struct {
	Kind       string
	Tag        string
	Type       string
	ListenPort uint16
	Config     stdjson.RawMessage
}

// MergeDashboardProfiles appends dashboard-owned server profiles before the
// Box constructs its runtime managers. Base configuration always wins tag
// ownership; collisions are rejected rather than silently replaced.
func MergeDashboardProfiles(ctx context.Context, options *option.Options) error {
	dataPaths := make(map[string]bool)
	for _, serviceOptions := range options.Services {
		if serviceOptions.Type != C.TypeAPI {
			continue
		}
		apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
		if !loaded || apiOptions.Dashboard == nil || !apiOptions.Dashboard.Enabled {
			continue
		}
		dataPath := apiOptions.Dashboard.DataPath
		if dataPath == "" {
			dataPath = defaultAdminDataPath
		}
		dataPaths[filemanager.BasePath(ctx, os.ExpandEnv(dataPath))] = true
	}
	if len(dataPaths) == 0 {
		return nil
	}

	usedTags := make(map[string]bool, len(options.Inbounds)+len(options.Endpoints))
	for _, inbound := range options.Inbounds {
		if inbound.Tag != "" {
			usedTags[inbound.Tag] = true
		}
	}
	for _, endpoint := range options.Endpoints {
		if endpoint.Tag != "" {
			usedTags[endpoint.Tag] = true
		}
	}

	paths := make([]string, 0, len(dataPaths))
	for dataPath := range dataPaths {
		paths = append(paths, dataPath)
	}
	sort.Strings(paths)
	for _, dataPath := range paths {
		stored, err := loadAdminStoreForProfiles(ctx, dataPath)
		if err != nil {
			return err
		}
		serverTags := make([]string, 0, len(stored.Servers))
		for tag := range stored.Servers {
			serverTags = append(serverTags, tag)
		}
		sort.Strings(serverTags)
		for _, tag := range serverTags {
			profile := stored.Servers[tag]
			if profile == nil {
				return E.New("invalid null dashboard server: ", tag)
			}
			if profile.Deleted {
				continue
			}
			if usedTags[tag] {
				return E.New("dashboard server tag collides with base configuration: ", tag)
			}
			switch profile.Kind {
			case adminServerKindInbound:
				var inbound option.Inbound
				if err = SJSON.UnmarshalContext(ctx, profile.Config, &inbound); err != nil {
					return E.Cause(err, "decode dashboard inbound ", tag)
				}
				if inbound.Tag != tag || inbound.Type != profile.Type {
					return E.New("dashboard inbound identity mismatch: ", tag)
				}
				options.Inbounds = append(options.Inbounds, inbound)
			case adminServerKindEndpoint:
				var endpoint option.Endpoint
				if err = SJSON.UnmarshalContext(ctx, profile.Config, &endpoint); err != nil {
					return E.Cause(err, "decode dashboard endpoint ", tag)
				}
				if endpoint.Tag != tag || endpoint.Type != profile.Type {
					return E.New("dashboard endpoint identity mismatch: ", tag)
				}
				options.Endpoints = append(options.Endpoints, endpoint)
			default:
				return E.New("unsupported dashboard server kind: ", profile.Kind)
			}
			usedTags[tag] = true
		}
	}
	return nil
}

func loadAdminStoreForProfiles(ctx context.Context, dataPath string) (adminStore, error) {
	content, err := filemanager.ReadFile(ctx, dataPath)
	if errors.Is(err, os.ErrNotExist) {
		content, err = filemanager.ReadFile(ctx, dataPath+".bak")
		if errors.Is(err, os.ErrNotExist) {
			return adminStore{}, nil
		}
	}
	if err != nil {
		return adminStore{}, E.Cause(err, "read dashboard data")
	}
	var stored adminStore
	if err = stdjson.Unmarshal(content, &stored); err != nil {
		return adminStore{}, E.Cause(err, "decode dashboard data")
	}
	if stored.Version != 1 && stored.Version != adminStoreVersion {
		return adminStore{}, E.New("unsupported dashboard data version: ", stored.Version)
	}
	return stored, nil
}

func (a *adminAPI) listProtocols(writer http.ResponseWriter, request *http.Request) {
	views := make([]adminProtocolView, 0, len(adminProtocolSpecs))
	for _, spec := range adminProtocolSpecs {
		if !adminProtocolAvailable(a.ctx, spec) {
			continue
		}
		template, err := buildAdminProtocolTemplate(spec)
		if err != nil {
			writeAdminError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		views = append(views, adminProtocolView{
			Kind: spec.Kind, Type: spec.Type, Name: C.ProxyDisplayName(spec.Type),
			Category: spec.Category, Network: spec.Network, TLS: spec.TLS,
			Credential: spec.Credential, UpdatePolicy: spec.UpdatePolicy,
			Description: spec.Description, Composite: spec.Composite, Template: template,
		})
	}
	writeAdminJSON(writer, http.StatusOK, map[string]any{"schema_version": 1, "protocols": views})
}

func adminProtocolAvailable(ctx context.Context, spec adminProtocolSpec) bool {
	switch spec.Kind {
	case adminServerKindInbound:
		registry := service.FromContext[option.InboundOptionsRegistry](ctx)
		if registry == nil {
			return false
		}
		_, loaded := registry.CreateOptions(spec.Type)
		return loaded
	case adminServerKindEndpoint:
		registry := service.FromContext[option.EndpointOptionsRegistry](ctx)
		if registry == nil {
			return false
		}
		_, loaded := registry.CreateOptions(spec.Type)
		return loaded
	default:
		return false
	}
}

func findAdminProtocolSpec(kind string, protocolType string) (adminProtocolSpec, bool) {
	for _, spec := range adminProtocolSpecs {
		if spec.Kind == kind && spec.Type == protocolType {
			return spec, true
		}
	}
	return adminProtocolSpec{}, false
}

func buildAdminProtocolTemplate(spec adminProtocolSpec) (stdjson.RawMessage, error) {
	password, err := randomAdminSecret(24)
	if err != nil {
		return nil, err
	}
	userID, err := newAdminID()
	if err != nil {
		return nil, err
	}
	tag := spec.Type + "-in"
	if spec.Kind == adminServerKindEndpoint {
		tag = spec.Type
	}
	base := map[string]any{"type": spec.Type, "tag": tag, "listen": "0.0.0.0"}
	tlsOptions := map[string]any{
		"enabled": true, "certificate_path": "/etc/sidera/tls/fullchain.pem", "key_path": "/etc/sidera/tls/private.key",
	}
	switch spec.Type {
	case C.TypeSOCKS:
		base["listen_port"] = 1080
		base["users"] = []any{map[string]any{"username": "user", "password": password}}
	case C.TypeHTTP:
		base["listen_port"] = 8080
		base["users"] = []any{map[string]any{"username": "user", "password": password}}
		base["tls"] = tlsOptions
	case C.TypeMixed:
		base["listen_port"] = 2080
		base["users"] = []any{map[string]any{"username": "user", "password": password}}
	case C.TypeShadowsocks:
		serverKey, keyErr := randomAdminKey(32)
		if keyErr != nil {
			return nil, keyErr
		}
		userKey, keyErr := randomAdminKey(32)
		if keyErr != nil {
			return nil, keyErr
		}
		base["listen_port"] = 8388
		base["method"] = "2022-blake3-aes-256-gcm"
		base["password"] = serverKey
		base["users"] = []any{map[string]any{"name": "user", "password": userKey}}
	case C.TypeSnell:
		base["listen_port"] = 8389
		base["version"] = 5
		base["psk"] = password
		base["users"] = []any{map[string]any{"name": "user", "userkey": password + "-user"}}
	case C.TypeVMess:
		base["listen_port"] = 10086
		base["users"] = []any{map[string]any{"name": "user", "uuid": userID, "alterId": 0}}
	case C.TypeTrojan:
		base["listen_port"] = 443
		base["users"] = []any{map[string]any{"name": "user", "password": password}}
		base["tls"] = tlsOptions
	case C.TypeNaive:
		base["listen_port"] = 8443
		base["network"] = []string{"tcp"}
		base["users"] = []any{map[string]any{"username": "user", "password": password}}
		base["tls"] = tlsOptions
	case C.TypeShadowTLS:
		base["listen_port"] = 9443
		base["version"] = 3
		base["users"] = []any{map[string]any{"name": "user", "password": password}}
		base["handshake"] = map[string]any{"server": "www.cloudflare.com", "server_port": 443}
	case C.TypeVLESS:
		base["listen_port"] = 443
		base["users"] = []any{map[string]any{"name": "user", "uuid": userID}}
		base["decryption"] = "none"
		base["tls"] = tlsOptions
	case C.TypeAnyTLS:
		base["listen_port"] = 8444
		base["users"] = []any{map[string]any{"name": "user", "password": password}}
		base["tls"] = tlsOptions
	case C.TypeHysteria:
		base["listen_port"] = 8445
		base["up_mbps"] = 100
		base["down_mbps"] = 100
		base["users"] = []any{map[string]any{"name": "user", "auth_str": password}}
		base["tls"] = tlsOptions
	case C.TypeTUIC:
		base["listen_port"] = 8446
		base["congestion_control"] = "bbr"
		base["users"] = []any{map[string]any{"name": "user", "uuid": userID, "password": password}}
		base["tls"] = tlsOptions
	case C.TypeHysteria2:
		base["listen_port"] = 8447
		base["up_mbps"] = 100
		base["down_mbps"] = 100
		base["users"] = []any{map[string]any{"name": "user", "password": password}}
		base["tls"] = tlsOptions
	case C.TypeOpenVPNServer:
		base["listen_port"] = 1194
		base["mode"] = "tls"
		base["network"] = "udp"
		base["address"] = []string{"10.8.0.1/24"}
		base["users"] = []any{map[string]any{"username": "user", "password": password}}
		base["tls"] = map[string]any{
			"certificate_path":          "/etc/sidera/openvpn/server.pem",
			"key_path":                  "/etc/sidera/openvpn/server.key",
			"client_certificate_path":   "/etc/sidera/openvpn/ca.pem",
			"verify_client_certificate": "require",
		}
	default:
		return nil, E.New("missing dashboard template for protocol: ", spec.Type)
	}
	return stdjson.MarshalIndent(base, "", "  ")
}

func randomAdminSecret(length int) (string, error) {
	buffer := make([]byte, length)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func randomAdminKey(length int) (string, error) {
	buffer := make([]byte, length)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buffer), nil
}

func (a *adminAPI) listServers(writer http.ResponseWriter, request *http.Request) {
	writeAdminJSON(writer, http.StatusOK, a.serverListResponse())
}

func (a *adminAPI) serverListResponse() map[string]any {
	viewsByTag := make(map[string]adminServerView, len(a.runtimes))
	for tag, runtimeInbound := range a.runtimes {
		view := adminServerView{
			Tag: tag, Type: runtimeInbound.Type, Kind: runtimeInbound.Kind,
			Source: "config", Status: "active", Managed: runtimeInbound.Manager != nil,
		}
		if runtimeInbound.Manager != nil {
			view.Credential = runtimeInbound.Manager.service.ManagedUserSchema().Credential
		}
		if spec, loaded := findAdminProtocolSpec(runtimeInbound.Kind, runtimeInbound.Type); loaded {
			view.UpdatePolicy = spec.UpdatePolicy
		}
		viewsByTag[tag] = view
	}

	restartRequired := false
	a.storeAccess.RLock()
	for tag, profile := range a.store.Servers {
		if profile == nil {
			continue
		}
		view := viewsByTag[tag]
		view.Tag = tag
		view.Type = profile.Type
		view.Kind = profile.Kind
		view.Source = "dashboard"
		view.Editable = true
		view.Advertise = profile.Advertise
		view.Config = append(stdjson.RawMessage(nil), profile.Config...)
		view.Revision = profile.Revision
		view.AppliedRevision = profile.AppliedRevision
		view.Pending = profile.Deleted || profile.Revision != profile.AppliedRevision
		if profile.Deleted {
			view.Status = "pending_delete"
		} else if _, active := a.runtimes[tag]; !active {
			view.Status = "pending_create"
		} else if view.Pending {
			view.Status = "pending_update"
		} else {
			view.Status = "active"
		}
		if spec, loaded := findAdminProtocolSpec(profile.Kind, profile.Type); loaded {
			view.Credential = spec.Credential
			view.Managed = spec.Credential != ""
			view.UpdatePolicy = spec.UpdatePolicy
		}
		restartRequired = restartRequired || view.Pending
		viewsByTag[tag] = view
	}
	a.storeAccess.RUnlock()

	views := make([]adminServerView, 0, len(viewsByTag))
	for _, view := range viewsByTag {
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Tag < views[j].Tag })
	return map[string]any{"servers": views, "restart_required": restartRequired}
}

func (a *adminAPI) createServer(writer http.ResponseWriter, request *http.Request) {
	var input adminServerInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	decoded, advertise, err := decodeAdminServer(a.ctx, input, "", "", "")
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, err.Error())
		return
	}
	a.mutation.Lock()
	defer a.mutation.Unlock()
	if a.runtimes[decoded.Tag] != nil {
		writeAdminError(writer, http.StatusConflict, "入站標籤已被基礎設定使用")
		return
	}
	a.storeAccess.Lock()
	if _, exists := a.store.Servers[decoded.Tag]; exists {
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusConflict, "入站標籤已存在")
		return
	}
	now := time.Now().UnixMilli()
	profile := &adminServerStore{
		Kind: decoded.Kind, Type: decoded.Type, Config: decoded.Config, Advertise: advertise,
		Revision: now, CreatedAt: now, UpdatedAt: now,
	}
	a.store.Servers[decoded.Tag] = profile
	a.storeAccess.Unlock()
	if err = a.saveStore(); err != nil {
		a.storeAccess.Lock()
		delete(a.store.Servers, decoded.Tag)
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusInternalServerError, E.Cause(err, "儲存入站失敗").Error())
		return
	}
	writeAdminJSON(writer, http.StatusCreated, a.serverListResponse())
}

func (a *adminAPI) updateServer(writer http.ResponseWriter, request *http.Request) {
	tag := chi.URLParam(request, "tag")
	var input adminServerInput
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	a.mutation.Lock()
	defer a.mutation.Unlock()
	a.storeAccess.RLock()
	current := cloneAdminServerStore(a.store.Servers[tag])
	a.storeAccess.RUnlock()
	if current == nil || current.Deleted {
		writeAdminError(writer, http.StatusNotFound, "找不到可編輯的面板入站")
		return
	}
	decoded, advertise, err := decodeAdminServer(a.ctx, input, tag, current.Kind, current.Type)
	if err != nil {
		writeAdminError(writer, http.StatusBadRequest, err.Error())
		return
	}
	updated := cloneAdminServerStore(current)
	updated.Config = decoded.Config
	updated.Advertise = advertise
	updated.Revision = max(current.Revision+1, time.Now().UnixMilli())
	updated.UpdatedAt = time.Now().UnixMilli()
	a.storeAccess.Lock()
	a.store.Servers[tag] = updated
	a.storeAccess.Unlock()
	if err = a.saveStore(); err != nil {
		a.storeAccess.Lock()
		a.store.Servers[tag] = current
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusInternalServerError, E.Cause(err, "儲存入站失敗").Error())
		return
	}
	writeAdminJSON(writer, http.StatusOK, a.serverListResponse())
}

func (a *adminAPI) deleteServer(writer http.ResponseWriter, request *http.Request) {
	tag := chi.URLParam(request, "tag")
	a.mutation.Lock()
	defer a.mutation.Unlock()
	a.storeAccess.Lock()
	current := cloneAdminServerStore(a.store.Servers[tag])
	if current == nil {
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusNotFound, "只能刪除由面板建立的入站")
		return
	}
	if a.runtimes[tag] == nil {
		delete(a.store.Servers, tag)
		delete(a.store.Inbounds, tag)
	} else {
		updated := cloneAdminServerStore(current)
		updated.Deleted = true
		updated.Revision = max(current.Revision+1, time.Now().UnixMilli())
		updated.UpdatedAt = time.Now().UnixMilli()
		a.store.Servers[tag] = updated
	}
	a.storeAccess.Unlock()
	if err := a.saveStore(); err != nil {
		a.storeAccess.Lock()
		a.store.Servers[tag] = current
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusInternalServerError, E.Cause(err, "儲存入站失敗").Error())
		return
	}
	writeAdminJSON(writer, http.StatusOK, a.serverListResponse())
}

func decodeAdminServer(ctx context.Context, input adminServerInput, expectedTag string, expectedKind string, expectedType string) (decodedAdminServer, adminServerAdvertise, error) {
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	if input.Kind == "" {
		input.Kind = adminServerKindInbound
	}
	if !stdjson.Valid(input.Config) {
		return decodedAdminServer{}, input.Advertise, E.New("入站 JSON 格式不正確")
	}
	var header struct {
		Type       string `json:"type"`
		Tag        string `json:"tag"`
		ListenPort uint16 `json:"listen_port"`
	}
	if err := stdjson.Unmarshal(input.Config, &header); err != nil {
		return decodedAdminServer{}, input.Advertise, E.Cause(err, "解析入站 JSON")
	}
	header.Type = strings.ToLower(strings.TrimSpace(header.Type))
	header.Tag = strings.TrimSpace(header.Tag)
	if header.Tag == "" || len(header.Tag) > 128 || strings.ContainsAny(header.Tag, "\x00\r\n/\\") {
		return decodedAdminServer{}, input.Advertise, E.New("入站 tag 必須是 1 至 128 字元，且不可包含斜線或控制字元")
	}
	if header.ListenPort == 0 {
		return decodedAdminServer{}, input.Advertise, E.New("listen_port 必須介於 1 至 65535")
	}
	if expectedTag != "" && header.Tag != expectedTag {
		return decodedAdminServer{}, input.Advertise, E.New("不可變更入站 tag")
	}
	if expectedKind != "" && input.Kind != expectedKind {
		return decodedAdminServer{}, input.Advertise, E.New("不可變更入站種類")
	}
	if expectedType != "" && header.Type != expectedType {
		return decodedAdminServer{}, input.Advertise, E.New("不可變更入站協議")
	}
	spec, supported := findAdminProtocolSpec(input.Kind, header.Type)
	if !supported || !adminProtocolAvailable(ctx, spec) {
		return decodedAdminServer{}, input.Advertise, E.New("此 Core build 不支援可管理的 server 協議: ", header.Type)
	}
	var canonical []byte
	var err error
	switch input.Kind {
	case adminServerKindInbound:
		var inbound option.Inbound
		if err = SJSON.UnmarshalContext(ctx, input.Config, &inbound); err == nil {
			canonical, err = SJSON.MarshalContext(ctx, &inbound)
		}
	case adminServerKindEndpoint:
		var endpoint option.Endpoint
		if err = SJSON.UnmarshalContext(ctx, input.Config, &endpoint); err == nil {
			canonical, err = SJSON.MarshalContext(ctx, &endpoint)
		}
	default:
		return decodedAdminServer{}, input.Advertise, E.New("未知的 server 種類: ", input.Kind)
	}
	if err != nil {
		return decodedAdminServer{}, input.Advertise, E.Cause(err, "驗證入站設定")
	}
	input.Advertise.Server = strings.TrimSpace(input.Advertise.Server)
	input.Advertise.TLSServerName = strings.TrimSpace(input.Advertise.TLSServerName)
	if strings.ContainsAny(input.Advertise.Server, "\x00\r\n/ ") || strings.ContainsAny(input.Advertise.TLSServerName, "\x00\r\n/ ") {
		return decodedAdminServer{}, input.Advertise, E.New("公開主機與 TLS Server Name 格式不正確")
	}
	if input.Advertise.Server != "" && input.Advertise.ServerPort == 0 {
		input.Advertise.ServerPort = header.ListenPort
	}
	return decodedAdminServer{
		Kind: input.Kind, Tag: header.Tag, Type: header.Type, ListenPort: header.ListenPort,
		Config: append(stdjson.RawMessage(nil), canonical...),
	}, input.Advertise, nil
}

func cloneAdminServerStore(profile *adminServerStore) *adminServerStore {
	if profile == nil {
		return nil
	}
	copyProfile := *profile
	copyProfile.Config = append(stdjson.RawMessage(nil), profile.Config...)
	return &copyProfile
}

func (a *adminAPI) reloadCore(writer http.ResponseWriter, request *http.Request) {
	response := a.serverListResponse()
	restartRequired, _ := response["restart_required"].(bool)
	if !restartRequired {
		writeAdminJSON(writer, http.StatusOK, map[string]any{"reloading": false, "message": "沒有待套用的入站變更"})
		return
	}
	if runtime.GOOS == "windows" {
		writeAdminError(writer, http.StatusNotImplemented, "Windows 目前需要由服務管理器重新啟動 Core")
		return
	}
	writer.Header().Set("Connection", "close")
	writeAdminJSON(writer, http.StatusAccepted, map[string]any{"reloading": true})
	go func() {
		time.Sleep(300 * time.Millisecond)
		process, err := os.FindProcess(os.Getpid())
		if err == nil {
			err = process.Signal(syscall.SIGHUP)
		}
		if err != nil {
			a.logger.Error("dashboard: request core reload: ", err)
		}
	}()
}
