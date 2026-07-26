package snell

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/Miku0139oao/sidera-core/common/uot"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	snellprotocol "github.com/sagernet/sing-snell"
	"github.com/sagernet/sing-snell/snellv5"
	"github.com/sagernet/sing-snell/snellv6"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.SnellInboundOptions](registry, C.TypeSnell, NewInbound)
}

var (
	_ adapter.TCPInjectableInbound = (*Inbound)(nil)
	_ adapter.ManagedUserService   = (*Inbound)(nil)
)

type snellUserStateKey struct{}

type snellUserState struct {
	service   snellprotocol.Service
	users     []adapter.ManagedUser
	userNames map[string]string
}

type Inbound struct {
	inbound.Adapter
	router    adapter.ConnectionRouterEx
	logger    logger.ContextLogger
	listener  *listener.Listener
	version   int
	v5Options snellv5.ServiceOptions
	v6Options snellv6.ServerOptions
	userState atomic.Pointer[snellUserState]
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.SnellInboundOptions) (adapter.Inbound, error) {
	inbound := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeSnell, tag),
		router:  uot.NewRouter(router, logger),
		logger:  logger,
		version: options.Version,
	}
	var err error
	switch options.Version {
	case 5:
		var obfsMode snellprotocol.ObfsMode
		obfsMode, err = snellprotocol.ParseObfsMode(options.ObfsOptions.ObfsMode)
		if err != nil {
			return nil, err
		}
		inbound.v5Options = snellv5.ServiceOptions{
			PSK:      []byte(options.PSK),
			ObfsMode: obfsMode,
			Handler:  inbound,
		}
	case 6:
		var mode snellv6.Mode
		mode, err = snellv6.ParseMode(options.V6Options.Mode)
		if err != nil {
			return nil, err
		}
		inbound.v6Options = snellv6.ServerOptions{
			PSK:     []byte(options.PSK),
			Mode:    mode,
			Handler: inbound,
		}
	case 0:
		return nil, E.New("snell: missing version")
	default:
		return nil, E.New("snell: unsupported version: ", options.Version)
	}
	if err != nil {
		return nil, err
	}
	managedUsers := make([]adapter.ManagedUser, len(options.Users))
	for index, user := range options.Users {
		managedUsers[index] = adapter.ManagedUser{Name: user.Name, Password: user.UserKey}
	}
	state, err := inbound.buildUserState(managedUsers)
	if err != nil {
		return nil, err
	}
	inbound.userState.Store(state)
	inbound.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: inbound,
	})
	return inbound, nil
}

func (h *Inbound) ManagedUserSchema() adapter.ManagedUserSchema {
	return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialPassword}
}

func (h *Inbound) ManagedUsers() []adapter.ManagedUser {
	return append([]adapter.ManagedUser(nil), h.userState.Load().users...)
}

func (h *Inbound) UpdateManagedUsers(users []adapter.ManagedUser) error {
	state, err := h.buildUserState(users)
	if err != nil {
		return err
	}
	h.userState.Store(state)
	return nil
}

func (h *Inbound) buildUserState(users []adapter.ManagedUser) (*snellUserState, error) {
	managedUsers := make([]adapter.ManagedUser, len(users))
	userIDs := make([]string, len(users))
	userKeys := make([][]byte, len(users))
	userNames := make(map[string]string, len(users))
	for index, user := range users {
		if user.Password == "" {
			return nil, E.New("missing user key for user ", index)
		}
		if _, loaded := userNames[user.Password]; loaded {
			return nil, E.New("duplicate user key for user ", index)
		}
		managedUsers[index] = adapter.ManagedUser{Name: user.Name, Password: user.Password}
		userIDs[index] = user.Password
		userKeys[index] = []byte(user.Password)
		userNames[user.Password] = user.Name
	}

	var (
		service snellprotocol.Service
		err     error
	)
	switch h.version {
	case 5:
		if len(users) == 0 {
			service, err = snellv5.NewService(h.v5Options)
		} else {
			multiService, serviceErr := snellv5.NewMultiService[string](h.v5Options)
			if serviceErr == nil {
				serviceErr = multiService.UpdateUsers(userIDs, userKeys)
			}
			service, err = multiService, serviceErr
		}
	case 6:
		if len(users) == 0 {
			service, err = snellv6.NewService(h.v6Options)
		} else {
			multiService, serviceErr := snellv6.NewMultiService[string](h.v6Options)
			if serviceErr == nil {
				serviceErr = multiService.UpdateUsers(userIDs, userKeys)
			}
			service, err = multiService, serviceErr
		}
	default:
		return nil, E.New("snell: unsupported version: ", h.version)
	}
	if err != nil {
		return nil, err
	}
	return &snellUserState{service: service, users: managedUsers, userNames: userNames}, nil
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

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	state := h.userState.Load()
	ctx = context.WithValue(adapter.WithContext(ctx, &metadata), snellUserStateKey{}, state)
	err := state.service.NewConnection(ctx, conn, metadata.Source, onClose)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		if E.IsClosedOrCanceled(err) {
			h.logger.DebugContext(ctx, "connection closed: ", err)
		} else {
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
		}
	}
}

func (h *Inbound) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	_, metadata := adapter.ExtendContext(ctx)
	if source.IsValid() {
		metadata.Source = source
	}
	if destination.IsValid() {
		metadata.Destination = destination
	}
	h.newConnection(ctx, conn, *metadata, onClose)
}

func (h *Inbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	_, metadata := adapter.ExtendContext(ctx)
	if source.IsValid() {
		metadata.Source = source
	}
	if destination.IsValid() {
		metadata.Destination = destination
	}
	h.newPacketConnection(ctx, conn, *metadata, onClose)
}

func (h *Inbound) newConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	state := h.userStateFromContext(ctx)
	if len(state.users) > 0 {
		userID, loaded := auth.UserFromContext[string](ctx)
		if !loaded {
			N.CloseOnHandshakeFailure(conn, onClose, E.New("missing authenticated user"))
			return
		}
		user := state.userNames[userID]
		if user == "" {
			user = "user"
		} else {
			metadata.User = user
		}
		h.logger.InfoContext(ctx, "[", user, "] inbound connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) newPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	state := h.userStateFromContext(ctx)
	if len(state.users) > 0 {
		userID, loaded := auth.UserFromContext[string](ctx)
		if !loaded {
			N.CloseOnHandshakeFailure(conn, onClose, E.New("missing authenticated user"))
			return
		}
		user := state.userNames[userID]
		if user == "" {
			user = "user"
		} else {
			metadata.User = user
		}
		h.logger.InfoContext(ctx, "[", user, "] inbound packet connection from ", metadata.Source)
	} else {
		h.logger.InfoContext(ctx, "inbound packet connection from ", metadata.Source)
	}
	h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) userStateFromContext(ctx context.Context) *snellUserState {
	state, _ := ctx.Value(snellUserStateKey{}).(*snellUserState)
	if state == nil {
		state = h.userState.Load()
	}
	return state
}
