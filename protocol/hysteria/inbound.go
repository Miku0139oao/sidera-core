package hysteria

import (
	"context"
	"errors"
	"maps"
	"net"
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
	"github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.HysteriaInboundOptions](registry, C.TypeHysteria, NewInbound)
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
	service         *hysteria.Service[string]
	serviceOptions  hysteria.ServiceOptions
	serviceAccess   sync.Mutex
	started         bool
	listenerAddress atomic.Pointer[M.Socksaddr]
	userAccess      sync.RWMutex
	users           []adapter.ManagedUser
	userNameMap     map[string]string
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.HysteriaInboundOptions) (adapter.Inbound, error) {
	options.UDPFragmentDefault = true
	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, err
	}
	inbound := &Inbound{
		Adapter:       inbound.NewAdapter(C.TypeHysteria, tag),
		ctx:           ctx,
		router:        router,
		logger:        logger,
		listenOptions: options.ListenOptions,
		tlsConfig:     tlsConfig,
	}
	var sendBps, receiveBps uint64
	if options.Up.Value() > 0 {
		sendBps = options.Up.Value()
	} else {
		sendBps = uint64(options.UpMbps) * hysteria.MbpsToBps
	}
	if options.Down.Value() > 0 {
		receiveBps = options.Down.Value()
	} else {
		receiveBps = uint64(options.DownMbps) * hysteria.MbpsToBps
	}
	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}
	inbound.serviceOptions = hysteria.ServiceOptions{
		Context:       ctx,
		Logger:        logger,
		SendBPS:       sendBps,
		ReceiveBPS:    receiveBps,
		XPlusPassword: options.Obfs,
		TLSConfig:     tlsConfig,
		QUICOptions:   buildInboundQUICOptions(options),
		UDPTimeout:    udpTimeout,
		Handler:       inbound,
	}
	users := make([]adapter.ManagedUser, len(options.Users))
	for index, user := range options.Users {
		var password string
		if user.AuthString != "" {
			password = user.AuthString
		} else {
			password = string(user.Auth)
		}
		users[index] = adapter.ManagedUser{
			Name:     user.Name,
			Password: password,
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
	maps.Copy(h.userNameMap, userNameMap)
	h.userAccess.Unlock()
	if err = h.replaceServiceLocked(service, oldUsers); err != nil {
		return err
	}
	h.userAccess.Lock()
	h.users = managedUsers
	h.userAccess.Unlock()
	return nil
}

func (h *Inbound) buildService(users []adapter.ManagedUser) (*hysteria.Service[string], []adapter.ManagedUser, map[string]string, error) {
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
	service, err := hysteria.NewService[string](h.serviceOptions)
	if err != nil {
		return nil, nil, nil, err
	}
	service.UpdateUsers(userList, passwordList)
	return service, managedUsers, userNameMap, nil
}

func (h *Inbound) replaceServiceLocked(newService *hysteria.Service[string], oldUsers []adapter.ManagedUser) error {
	if !h.started {
		if h.service != nil {
			_ = h.service.Close()
		}
		h.service = newService
		return nil
	}
	oldService := h.service
	oldListener := h.listener
	_ = oldService.Close()
	_ = oldListener.Close()
	newListener, err := h.startService(newService)
	if err == nil {
		h.service = newService
		h.listener = newListener
		return nil
	}
	rollbackService, _, _, rollbackBuildErr := h.buildService(oldUsers)
	if rollbackBuildErr != nil {
		return errors.Join(err, E.Cause(rollbackBuildErr, "rebuild previous hysteria service"))
	}
	rollbackListener, rollbackErr := h.startService(rollbackService)
	if rollbackErr != nil {
		return errors.Join(err, E.Cause(rollbackErr, "restore previous hysteria service"))
	}
	h.service = rollbackService
	h.listener = rollbackListener
	return err
}

func (h *Inbound) startService(service *hysteria.Service[string]) (*listener.Listener, error) {
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
