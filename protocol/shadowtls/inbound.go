package shadowtls

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/common/dialer"
	"github.com/Miku0139oao/sidera-core/common/listener"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing-shadowtls"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.ShadowTLSInboundOptions](registry, C.TypeShadowTLS, NewInbound)
}

var (
	_ adapter.TCPInjectableInbound = (*Inbound)(nil)
	_ adapter.ManagedUserService   = (*Inbound)(nil)
)

type serviceState struct {
	service *shadowtls.Service
	users   []adapter.ManagedUser
}

type Inbound struct {
	inbound.Adapter
	router        adapter.Router
	logger        logger.ContextLogger
	listener      *listener.Listener
	version       int
	serviceConfig shadowtls.ServiceConfig
	updateAccess  sync.Mutex
	serviceState  atomic.Pointer[serviceState]
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowTLSInboundOptions) (adapter.Inbound, error) {
	inbound := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeShadowTLS, tag),
		router:  router,
		logger:  logger,
	}

	if options.Version == 0 {
		options.Version = 1
	}

	var handshakeForServerName map[string]shadowtls.HandshakeConfig
	if options.Version > 1 {
		handshakeForServerName = make(map[string]shadowtls.HandshakeConfig)
		if options.HandshakeForServerName != nil {
			for _, entry := range options.HandshakeForServerName.Entries() {
				handshakeDialer, err := dialer.New(ctx, entry.Value.DialerOptions, entry.Value.ServerIsDomain())
				if err != nil {
					return nil, err
				}
				handshakeForServerName[entry.Key] = shadowtls.HandshakeConfig{
					Server: entry.Value.ServerOptions.Build(),
					Dialer: handshakeDialer,
				}
			}
		}
	}
	serverIsDomain := options.Handshake.ServerIsDomain()
	if options.WildcardSNI != option.ShadowTLSWildcardSNIOff {
		serverIsDomain = true
	}
	handshakeDialer, err := dialer.New(ctx, options.Handshake.DialerOptions, serverIsDomain)
	if err != nil {
		return nil, err
	}
	serviceUsers := common.Map(options.Users, func(it option.ShadowTLSUser) shadowtls.User {
		return (shadowtls.User)(it)
	})
	var managedUsers []adapter.ManagedUser
	if options.Version == 3 {
		users := make([]adapter.ManagedUser, len(options.Users))
		for index, user := range options.Users {
			users[index] = adapter.ManagedUser{Name: user.Name, Password: user.Password}
		}
		managedUsers, serviceUsers, err = buildUsers(users)
		if err != nil {
			return nil, err
		}
	}
	serviceConfig := shadowtls.ServiceConfig{
		Version:  options.Version,
		Password: options.Password,
		Users:    serviceUsers,
		Handshake: shadowtls.HandshakeConfig{
			Server: options.Handshake.ServerOptions.Build(),
			Dialer: handshakeDialer,
		},
		HandshakeForServerName: handshakeForServerName,
		StrictMode:             options.StrictMode,
		WildcardSNI:            shadowtls.WildcardSNI(options.WildcardSNI),
		Handler:                (*inboundHandler)(inbound),
		Logger:                 logger,
	}
	service, err := shadowtls.NewService(serviceConfig)
	if err != nil {
		return nil, err
	}
	inbound.version = options.Version
	inbound.serviceConfig = serviceConfig
	inbound.serviceState.Store(&serviceState{service: service, users: managedUsers})
	inbound.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: inbound,
	})
	return inbound, nil
}

func (h *Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return h.listener.Start()
}

func (h *Inbound) Close() error {
	return h.listener.Close()
}

func (h *Inbound) ManagedUserSchema() adapter.ManagedUserSchema {
	if h.version != 3 {
		return adapter.ManagedUserSchema{}
	}
	return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialPassword}
}

func (h *Inbound) ManagedUsers() []adapter.ManagedUser {
	if h.version != 3 {
		return nil
	}
	users := h.serviceState.Load().users
	return append([]adapter.ManagedUser(nil), users...)
}

func (h *Inbound) UpdateManagedUsers(users []adapter.ManagedUser) error {
	if h.version != 3 {
		return E.New("managed users are only supported by ShadowTLS version 3")
	}
	usersCopy, serviceUsers, err := buildUsers(users)
	if err != nil {
		return err
	}

	h.updateAccess.Lock()
	defer h.updateAccess.Unlock()
	serviceConfig := h.serviceConfig
	serviceConfig.Users = serviceUsers
	service, err := shadowtls.NewService(serviceConfig)
	if err != nil {
		return err
	}
	h.serviceConfig = serviceConfig
	h.serviceState.Store(&serviceState{service: service, users: usersCopy})
	return nil
}

func buildUsers(users []adapter.ManagedUser) ([]adapter.ManagedUser, []shadowtls.User, error) {
	names := make(map[string]struct{}, len(users))
	usersCopy := make([]adapter.ManagedUser, len(users))
	serviceUsers := make([]shadowtls.User, len(users))
	for index, user := range users {
		if user.Name == "" {
			return nil, nil, E.New("missing name for user[", index, "]")
		}
		if user.Password == "" {
			return nil, nil, E.New("missing password for user[", index, "]")
		}
		if _, exists := names[user.Name]; exists {
			return nil, nil, E.New("duplicate name for user[", index, "]: ", user.Name)
		}
		names[user.Name] = struct{}{}
		usersCopy[index] = adapter.ManagedUser{Name: user.Name, Password: user.Password}
		serviceUsers[index] = shadowtls.User{Name: user.Name, Password: user.Password}
	}
	return usersCopy, serviceUsers, nil
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	service := h.serviceState.Load().service
	err := service.NewConnection(adapter.WithContext(log.ContextWithNewID(ctx), &metadata), conn, metadata.Source, metadata.Destination, onClose)
	N.CloseOnHandshakeFailure(conn, onClose, err)
	if err != nil {
		if E.IsClosedOrCanceled(err) {
			h.logger.DebugContext(ctx, "connection closed: ", err)
		} else {
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
		}
	}
}

type inboundHandler Inbound

func (h *inboundHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.Source = source
	metadata.Destination = destination
	if userName, _ := auth.UserFromContext[string](ctx); userName != "" {
		metadata.User = userName
		h.logger.InfoContext(ctx, "[", userName, "] inbound connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}
