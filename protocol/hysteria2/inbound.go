package hysteria2

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/Miku0139oao/sidera-core/common/tls"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	qtls "github.com/sagernet/sing-quic"
	"github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing-quic/hysteria2/realm"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.Hysteria2InboundOptions](registry, C.TypeHysteria2, NewInbound)
}

var _ adapter.ManagedUserService = (*Inbound)(nil)

type Inbound struct {
	inbound.Adapter
	ctx             context.Context
	router          adapter.Router
	logger          log.ContextLogger
	listenOptions   option.ListenOptions
	listener        *listener.Listener
	tlsConfig       tls.ServerConfig
	service         *hysteria2.Service[string]
	serviceOptions  hysteria2.ServiceOptions
	serviceAccess   sync.Mutex
	started         bool
	listenerAddress atomic.Pointer[M.Socksaddr]
	userAccess      sync.RWMutex
	users           []adapter.ManagedUser
	userNameMap     map[string]string
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.Hysteria2InboundOptions) (adapter.Inbound, error) {
	options.UDPFragmentDefault = true
	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, err
	}
	var salamanderPassword string
	var geckoPassword string
	var geckoMinPacketSize, geckoMaxPacketSize int
	if options.Obfs != nil {
		if options.Obfs.Password == "" {
			return nil, E.New("missing obfs password")
		}
		switch options.Obfs.Type {
		case hysteria2.ObfsTypeSalamander:
			salamanderPassword = options.Obfs.Password
		case hysteria2.ObfsTypeGecko:
			geckoPassword = options.Obfs.Password
			geckoMinPacketSize = options.Obfs.GeckoOptions.MinPacketSize
			geckoMaxPacketSize = options.Obfs.GeckoOptions.MaxPacketSize
		default:
			return nil, E.New("unknown obfs type: ", options.Obfs.Type)
		}
	}
	var masqueradeHandler http.Handler
	if options.Masquerade != nil && options.Masquerade.Type != "" {
		switch options.Masquerade.Type {
		case C.Hysterai2MasqueradeTypeFile:
			masqueradeDirectory := filemanager.BasePath(ctx, os.ExpandEnv(options.Masquerade.FileOptions.Directory))
			_, err = filemanager.ReadDir(ctx, masqueradeDirectory)
			if err != nil && !os.IsNotExist(err) {
				return nil, E.Cause(err, "read masquerade directory")
			}
			masqueradeHandler = http.FileServer(http.Dir(masqueradeDirectory))
		case C.Hysterai2MasqueradeTypeProxy:
			masqueradeURL, err := url.Parse(options.Masquerade.ProxyOptions.URL)
			if err != nil {
				return nil, E.Cause(err, "parse masquerade URL")
			}
			masqueradeHandler = &httputil.ReverseProxy{
				Rewrite: func(r *httputil.ProxyRequest) {
					r.SetURL(masqueradeURL)
					if !options.Masquerade.ProxyOptions.RewriteHost {
						r.Out.Host = r.In.Host
					}
				},
				ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
					w.WriteHeader(http.StatusBadGateway)
				},
			}
		case C.Hysterai2MasqueradeTypeString:
			masqueradeHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if options.Masquerade.StringOptions.StatusCode != 0 {
					w.WriteHeader(options.Masquerade.StringOptions.StatusCode)
				}
				for key, values := range options.Masquerade.StringOptions.Headers {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.Write([]byte(options.Masquerade.StringOptions.Content))
			})
		default:
			return nil, E.New("unknown masquerade type: ", options.Masquerade.Type)
		}
	}
	inbound := &Inbound{
		Adapter:       inbound.NewAdapter(C.TypeHysteria2, tag),
		ctx:           ctx,
		router:        router,
		logger:        logger,
		listenOptions: options.ListenOptions,
		tlsConfig:     tlsConfig,
	}
	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}
	var realmOptions *realm.Options
	if options.Realm != nil {
		if options.Realm.IPVersion != 0 && options.ListenOptions.Listen != nil {
			listenAddr := netip.Addr(*options.ListenOptions.Listen).Unmap()
			if options.Realm.IPVersion == 6 && listenAddr.Is4() {
				return nil, E.New("realm.ip_version 6 conflicts with listen address ", listenAddr)
			}
			if options.Realm.IPVersion == 4 && listenAddr.Is6() && !listenAddr.IsUnspecified() {
				return nil, E.New("realm.ip_version 4 conflicts with listen address ", listenAddr)
			}
		}
		queryOptions, err := adapter.DNSQueryOptionsFrom(ctx, options.Realm.STUNDomainResolver)
		if err != nil {
			return nil, err
		}
		httpClientTransport, err := service.FromContext[adapter.HTTPClientManager](ctx).ResolveTransport(ctx, logger, common.PtrValueOrDefault(options.Realm.HTTPClient))
		if err != nil {
			return nil, E.Cause(err, "create realm http client")
		}
		dnsRouter := service.FromContext[adapter.DNSRouter](ctx)
		realmOptions = &realm.Options{
			ServerURL:   options.Realm.ServerURL,
			Token:       options.Realm.Token,
			RealmID:     options.Realm.RealmID,
			STUNServers: options.Realm.STUNServers,
			HTTPClient:  &http.Client{Transport: httpClientTransport},
			Resolver: func(ctx context.Context, host string, ipv4, ipv6 bool) ([]netip.Addr, error) {
				dnsOptions := queryOptions
				switch {
				case ipv4 && !ipv6:
					dnsOptions.Strategy = C.DomainStrategyIPv4Only
				case !ipv4 && ipv6:
					dnsOptions.Strategy = C.DomainStrategyIPv6Only
				}
				return dnsRouter.Lookup(ctx, host, dnsOptions)
			},
			Logger:    logger,
			IPVersion: options.Realm.IPVersion,
		}
		if options.Realm.PortMapping != nil && options.Realm.PortMapping.Enabled {
			realmOptions.PortMapping = &realm.PortMappingOptions{
				Timeout:  time.Duration(options.Realm.PortMapping.Timeout),
				Lifetime: time.Duration(options.Realm.PortMapping.Lifetime),
			}
		}
	}
	inbound.serviceOptions = hysteria2.ServiceOptions{
		Context:            ctx,
		Logger:             logger,
		BrutalDebug:        options.BrutalDebug,
		SendBPS:            uint64(options.UpMbps * hysteria.MbpsToBps),
		ReceiveBPS:         uint64(options.DownMbps * hysteria.MbpsToBps),
		SalamanderPassword: salamanderPassword,
		GeckoPassword:      geckoPassword,
		GeckoMinPacketSize: geckoMinPacketSize,
		GeckoMaxPacketSize: geckoMaxPacketSize,
		TLSConfig:          tlsConfig,
		QUICOptions: qtls.QUICOptions{
			IdleTimeout:             options.IdleTimeout.Build(),
			KeepAlivePeriod:         options.KeepAlivePeriod.Build(),
			StreamReceiveWindow:     options.StreamReceiveWindow.Value(),
			ConnectionReceiveWindow: options.ConnectionReceiveWindow.Value(),
			MaxConcurrentStreams:    options.MaxConcurrentStreams,
			InitialPacketSize:       options.InitialPacketSize,
			DisablePathMTUDiscovery: options.DisablePathMTUDiscovery,
		},
		IgnoreClientBandwidth: options.IgnoreClientBandwidth,
		UDPTimeout:            udpTimeout,
		Handler:               inbound,
		MasqueradeHandler:     masqueradeHandler,
		BBRProfile:            options.BBRProfile,
		RealmOptions:          realmOptions,
	}
	users := make([]adapter.ManagedUser, len(options.Users))
	for index, user := range options.Users {
		users[index] = adapter.ManagedUser{
			Name:     user.Name,
			Password: user.Password,
		}
	}
	if err = inbound.UpdateManagedUsers(users); err != nil {
		return nil, err
	}
	return inbound, nil
}

func (h *Inbound) ManagedUserSchema() adapter.ManagedUserSchema {
	return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialPassword}
}

func (h *Inbound) ManagedUsers() []adapter.ManagedUser {
	h.userAccess.RLock()
	defer h.userAccess.RUnlock()
	return append([]adapter.ManagedUser(nil), h.users...)
}

func (h *Inbound) UpdateManagedUsers(users []adapter.ManagedUser) error {
	service, managedUsers, userNameMap, err := h.buildService(users)
	if err != nil {
		return err
	}
	h.serviceAccess.Lock()
	defer h.serviceAccess.Unlock()
	oldUsers := h.ManagedUsers()
	h.userAccess.Lock()
	if h.userNameMap == nil {
		h.userNameMap = make(map[string]string)
	}
	for credential, name := range userNameMap {
		h.userNameMap[credential] = name
	}
	h.userAccess.Unlock()
	if err = h.replaceServiceLocked(service, oldUsers); err != nil {
		return err
	}
	h.userAccess.Lock()
	h.users = managedUsers
	h.userAccess.Unlock()
	return nil
}

func (h *Inbound) buildService(users []adapter.ManagedUser) (*hysteria2.Service[string], []adapter.ManagedUser, map[string]string, error) {
	userList := make([]string, len(users))
	passwordList := make([]string, len(users))
	managedUsers := make([]adapter.ManagedUser, len(users))
	userNameMap := make(map[string]string, len(users))
	for index, user := range users {
		if user.Password == "" {
			return nil, nil, nil, E.New("missing password for user ", index)
		}
		userList[index] = user.Password
		passwordList[index] = user.Password
		managedUsers[index] = adapter.ManagedUser{
			Name:     user.Name,
			Password: user.Password,
		}
		userNameMap[user.Password] = user.Name
	}
	service, err := hysteria2.NewService[string](h.serviceOptions)
	if err != nil {
		return nil, nil, nil, err
	}
	service.UpdateUsers(userList, passwordList)
	return service, managedUsers, userNameMap, nil
}

func (h *Inbound) replaceServiceLocked(newService *hysteria2.Service[string], oldUsers []adapter.ManagedUser) error {
	if !h.started {
		if h.service != nil {
			_ = h.service.Close()
		}
		h.service = newService
		return nil
	}
	_ = h.service.Close()
	_ = h.listener.Close()
	newListener, err := h.startService(newService)
	if err == nil {
		h.service = newService
		h.listener = newListener
		return nil
	}
	rollbackService, _, _, rollbackBuildErr := h.buildService(oldUsers)
	if rollbackBuildErr != nil {
		return errors.Join(err, E.Cause(rollbackBuildErr, "rebuild previous hysteria2 service"))
	}
	rollbackListener, rollbackErr := h.startService(rollbackService)
	if rollbackErr != nil {
		return errors.Join(err, E.Cause(rollbackErr, "restore previous hysteria2 service"))
	}
	h.service = rollbackService
	h.listener = rollbackListener
	return err
}

func (h *Inbound) startService(service *hysteria2.Service[string]) (*listener.Listener, error) {
	newListener := listener.New(listener.Options{Context: h.ctx, Logger: h.logger, Listen: h.listenOptions})
	packetConn, err := newListener.ListenUDP()
	if err != nil {
		return nil, err
	}
	address := newListener.UDPAddr()
	h.listenerAddress.Store(&address)
	if err = service.Start(packetConn); err != nil {
		_ = newListener.Close()
		_ = service.Close()
		return nil, err
	}
	return newListener, nil
}

func (h *Inbound) userName(ctx context.Context) string {
	credential, loaded := auth.UserFromContext[string](ctx)
	if !loaded {
		return ""
	}
	h.userAccess.RLock()
	userName := h.userNameMap[credential]
	h.userAccess.RUnlock()
	return userName
}

func (h *Inbound) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	ctx = log.ContextWithNewID(ctx)
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listenOptions.Detour
	//nolint:staticcheck
	if address := h.listenerAddress.Load(); address != nil {
		metadata.OriginDestination = *address
	}
	metadata.Source = source
	metadata.Destination = destination
	h.logger.InfoContext(ctx, "inbound connection from ", metadata.Source)
	if userName := h.userName(ctx); userName != "" {
		metadata.User = userName
		h.logger.InfoContext(ctx, "[", userName, "] inbound connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	ctx = log.ContextWithNewID(ctx)
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listenOptions.Detour
	//nolint:staticcheck
	if address := h.listenerAddress.Load(); address != nil {
		metadata.OriginDestination = *address
	}
	metadata.Source = source
	metadata.Destination = destination
	h.logger.InfoContext(ctx, "inbound packet connection from ", metadata.Source)
	if userName := h.userName(ctx); userName != "" {
		metadata.User = userName
		h.logger.InfoContext(ctx, "[", userName, "] inbound packet connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound packet connection to ", metadata.Destination)
	}
	h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		err := h.tlsConfig.Start()
		if err != nil {
			return err
		}
	}
	h.serviceAccess.Lock()
	defer h.serviceAccess.Unlock()
	newListener, err := h.startService(h.service)
	if err != nil {
		return err
	}
	h.listener = newListener
	h.started = true
	return nil
}

func (h *Inbound) InterfaceUpdated() {
	h.serviceAccess.Lock()
	h.service.Reset()
	h.serviceAccess.Unlock()
}

func (h *Inbound) Close() error {
	h.serviceAccess.Lock()
	defer h.serviceAccess.Unlock()
	h.started = false
	return common.Close(
		common.PtrOrNil(h.listener),
		h.tlsConfig,
		common.PtrOrNil(h.service),
	)
}
