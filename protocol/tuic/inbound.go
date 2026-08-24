package tuic

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/Miku0139oao/sidera-core/common/tls"
	"github.com/Miku0139oao/sidera-core/common/uot"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	qtls "github.com/sagernet/sing-quic"
	"github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid/v5"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.TUICInboundOptions](registry, C.TypeTUIC, NewInbound)
}

var _ adapter.ManagedUserService = (*Inbound)(nil)

type authenticatedTUICUser struct {
	UUID string
	Name string
}

type Inbound struct {
	inbound.Adapter
	ctx             context.Context
	router          adapter.ConnectionRouterEx
	logger          log.ContextLogger
	listenOptions   option.ListenOptions
	listener        *listener.Listener
	tlsConfig       tls.ServerConfig
	server          *tuic.Service[authenticatedTUICUser]
	serviceOptions  tuic.ServiceOptions
	serviceAccess   sync.Mutex
	started         bool
	listenerAddress atomic.Pointer[M.Socksaddr]

	usersAccess  sync.RWMutex
	users        map[string]option.TUICUser
	managedUsers []adapter.ManagedUser
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TUICInboundOptions) (adapter.Inbound, error) {
	options.UDPFragmentDefault = true
	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, err
	}
	inbound := &Inbound{
		Adapter:       inbound.NewAdapter(C.TypeTUIC, tag),
		ctx:           ctx,
		router:        uot.NewRouter(router, logger),
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
	inbound.serviceOptions = tuic.ServiceOptions{
		Context:   ctx,
		Logger:    logger,
		TLSConfig: tlsConfig,
		QUICOptions: qtls.QUICOptions{
			IdleTimeout:             options.IdleTimeout.Build(),
			KeepAlivePeriod:         options.KeepAlivePeriod.Build(),
			StreamReceiveWindow:     options.StreamReceiveWindow.Value(),
			ConnectionReceiveWindow: options.ConnectionReceiveWindow.Value(),
			MaxConcurrentStreams:    options.MaxConcurrentStreams,
			InitialPacketSize:       options.InitialPacketSize,
			DisablePathMTUDiscovery: options.DisablePathMTUDiscovery,
		},
		CongestionControl: options.CongestionControl,
		AuthTimeout:       time.Duration(options.AuthTimeout),
		ZeroRTTHandshake:  options.ZeroRTTHandshake,
		Heartbeat:         time.Duration(options.Heartbeat),
		UDPTimeout:        udpTimeout,
		Handler:           inbound,
	}
	managedUsers := make([]adapter.ManagedUser, len(options.Users))
	for index, user := range options.Users {
		managedUsers[index] = adapter.ManagedUser{Name: user.Name, UUID: user.UUID, Password: user.Password}
	}
	err = inbound.UpdateManagedUsers(managedUsers)
	if err != nil {
		return nil, err
	}
	return inbound, nil
}

func (h *Inbound) ManagedUserSchema() adapter.ManagedUserSchema {
	return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialUUIDPassword}
}

func (h *Inbound) ManagedUsers() []adapter.ManagedUser {
	h.usersAccess.RLock()
	defer h.usersAccess.RUnlock()
	return append([]adapter.ManagedUser(nil), h.managedUsers...)
}

func (h *Inbound) UpdateManagedUsers(users []adapter.ManagedUser) error {
	h.serviceAccess.Lock()
	defer h.serviceAccess.Unlock()
	service, managedUsers, userMap, err := h.buildService(users)
	if err != nil {
		return err
	}
	return h.replaceServiceLocked(service, managedUsers, userMap, h.startService)
}

func (h *Inbound) buildService(users []adapter.ManagedUser) (*tuic.Service[authenticatedTUICUser], []adapter.ManagedUser, map[string]option.TUICUser, error) {
	userList := make([]authenticatedTUICUser, len(users))
	uuidList := make([][16]byte, len(users))
	passwordList := make([]string, len(users))
	userMap := make(map[string]option.TUICUser, len(users))
	managedUsers := make([]adapter.ManagedUser, len(users))
	for index, user := range users {
		if user.UUID == "" {
			return nil, nil, nil, E.New("missing uuid for user ", index)
		}
		if user.Password == "" {
			return nil, nil, nil, E.New("missing password for user ", index)
		}
		userUUID, err := uuid.FromString(user.UUID)
		if err != nil {
			return nil, nil, nil, E.Cause(err, "invalid uuid for user ", index)
		}
		userID := userUUID.String()
		if _, exists := userMap[userID]; exists {
			return nil, nil, nil, E.New("duplicate uuid for user ", index, ": ", userID)
		}
		protocolUser := option.TUICUser{Name: user.Name, UUID: user.UUID, Password: user.Password}
		// The service writes this generation's immutable identity into the
		// authenticated context; callbacks must not resolve through a newer map.
		userList[index] = authenticatedTUICUser{UUID: userID, Name: user.Name}
		uuidList[index] = userUUID
		passwordList[index] = user.Password
		userMap[userID] = protocolUser
		managedUsers[index] = adapter.ManagedUser{Name: user.Name, UUID: user.UUID, Password: user.Password}
	}
	service, err := tuic.NewService[authenticatedTUICUser](h.serviceOptions)
	if err != nil {
		return nil, nil, nil, err
	}
	service.UpdateUsers(userList, uuidList, passwordList)
	return service, managedUsers, userMap, nil
}

func (h *Inbound) replaceServiceLocked(
	newService *tuic.Service[authenticatedTUICUser],
	newUsers []adapter.ManagedUser,
	newUserMap map[string]option.TUICUser,
	startService func(*tuic.Service[authenticatedTUICUser]) (*listener.Listener, error),
) error {
	h.usersAccess.RLock()
	oldUsers := append([]adapter.ManagedUser(nil), h.managedUsers...)
	oldUserMap := h.users
	h.usersAccess.RUnlock()
	if !h.started {
		if h.server != nil {
			_ = h.server.Close()
		}
		h.server = newService
		h.publishUserGeneration(newUsers, newUserMap)
		return nil
	}
	if h.server != nil {
		_ = h.server.Close()
	}
	if h.listener != nil {
		_ = h.listener.Close()
	}

	// Publish UUID-to-identity metadata before Start can authenticate traffic.
	h.publishUserGeneration(newUsers, newUserMap)
	newListener, err := startService(newService)
	if err == nil {
		h.server = newService
		h.listener = newListener
		return nil
	}

	// Restore the previous metadata before the rollback listener admits traffic.
	h.publishUserGeneration(oldUsers, oldUserMap)
	rollbackService, _, _, rollbackBuildErr := h.buildService(oldUsers)
	if rollbackBuildErr != nil {
		return errors.Join(err, E.Cause(rollbackBuildErr, "rebuild previous TUIC service"))
	}
	rollbackListener, rollbackErr := startService(rollbackService)
	if rollbackErr != nil {
		return errors.Join(err, E.Cause(rollbackErr, "restore previous TUIC service"))
	}
	h.server = rollbackService
	h.listener = rollbackListener
	return err
}

func (h *Inbound) publishUserGeneration(users []adapter.ManagedUser, userMap map[string]option.TUICUser) {
	h.usersAccess.Lock()
	h.users = userMap
	h.managedUsers = users
	h.usersAccess.Unlock()
}

func (h *Inbound) startService(service *tuic.Service[authenticatedTUICUser]) (*listener.Listener, error) {
	newListener := listener.New(listener.Options{Context: h.ctx, Logger: h.logger, Listen: h.listenOptions})
	packetConn, err := newListener.ListenUDP()
	if err != nil {
		_ = newListener.Close()
		_ = service.Close()
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

func (h *Inbound) userName(userID string) string {
	h.usersAccess.RLock()
	user := h.users[userID]
	h.usersAccess.RUnlock()
	return user.Name
}

func authenticatedUserName(ctx context.Context) string {
	user, loaded := auth.UserFromContext[authenticatedTUICUser](ctx)
	if !loaded {
		return ""
	}
	return user.Name
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
	userName := authenticatedUserName(ctx)
	if userName != "" {
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
	userName := authenticatedUserName(ctx)
	if userName != "" {
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
	newListener, err := h.startService(h.server)
	if err != nil {
		return err
	}
	h.listener = newListener
	h.started = true
	return nil
}

func (h *Inbound) Close() error {
	h.serviceAccess.Lock()
	defer h.serviceAccess.Unlock()
	h.started = false
	return common.Close(
		common.PtrOrNil(h.listener),
		h.tlsConfig,
		common.PtrOrNil(h.server),
	)
}
