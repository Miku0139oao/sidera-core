package mixed

import (
	std_bufio "bufio"
	"context"
	"net"
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
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/http"
	"github.com/sagernet/sing/protocol/socks"
	"github.com/sagernet/sing/protocol/socks/socks4"
	"github.com/sagernet/sing/protocol/socks/socks5"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.HTTPMixedInboundOptions](registry, C.TypeMixed, NewInbound)
}

var (
	_ adapter.TCPInjectableInbound = (*Inbound)(nil)
	_ adapter.ManagedUserService   = (*Inbound)(nil)
)

type userState struct {
	users         []adapter.ManagedUser
	authenticator *auth.Authenticator
}

type Inbound struct {
	inbound.Adapter
	router     adapter.ConnectionRouterEx
	logger     log.ContextLogger
	listener   *listener.Listener
	userState  atomic.Pointer[userState]
	tlsConfig  tls.ServerConfig
	udpTimeout time.Duration
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.HTTPMixedInboundOptions) (adapter.Inbound, error) {
	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}
	users := make([]adapter.ManagedUser, len(options.Users))
	for index, user := range options.Users {
		users[index] = adapter.ManagedUser{Name: user.Username, Password: user.Password}
	}
	userState, err := newUserState(users)
	if err != nil {
		return nil, err
	}
	inbound := &Inbound{
		Adapter:    inbound.NewAdapter(C.TypeMixed, tag),
		router:     uot.NewRouter(router, logger),
		logger:     logger,
		udpTimeout: udpTimeout,
	}
	inbound.userState.Store(userState)
	if options.TLS != nil {
		tlsConfig, err := tls.NewServerWithOptions(tls.ServerOptions{
			Context:        ctx,
			Logger:         logger,
			Options:        common.PtrValueOrDefault(options.TLS),
			KTLSCompatible: true,
		})
		if err != nil {
			return nil, err
		}
		inbound.tlsConfig = tlsConfig
	}
	inbound.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: inbound,
		SetSystemProxy:    options.SetSystemProxy,
		SystemProxySOCKS:  true,
	})
	return inbound, nil
}

func (h *Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		err := h.tlsConfig.Start()
		if err != nil {
			return E.Cause(err, "create TLS config")
		}
	}
	return h.listener.Start()
}

func (h *Inbound) Close() error {
	return common.Close(
		h.listener,
		h.tlsConfig,
	)
}

func (h *Inbound) ManagedUserSchema() adapter.ManagedUserSchema {
	return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialPassword}
}

func (h *Inbound) ManagedUsers() []adapter.ManagedUser {
	users := h.userState.Load().users
	return append([]adapter.ManagedUser(nil), users...)
}

func (h *Inbound) UpdateManagedUsers(users []adapter.ManagedUser) error {
	userState, err := newUserState(users)
	if err != nil {
		return err
	}
	h.userState.Store(userState)
	return nil
}

func newUserState(users []adapter.ManagedUser) (*userState, error) {
	names := make(map[string]struct{}, len(users))
	usersCopy := make([]adapter.ManagedUser, len(users))
	authUsers := make([]auth.User, len(users))
	for index, user := range users {
		if user.Name == "" {
			return nil, E.New("missing name for user[", index, "]")
		}
		if user.Password == "" {
			return nil, E.New("missing password for user[", index, "]")
		}
		if _, exists := names[user.Name]; exists {
			return nil, E.New("duplicate name for user[", index, "]: ", user.Name)
		}
		names[user.Name] = struct{}{}
		usersCopy[index] = adapter.ManagedUser{Name: user.Name, Password: user.Password}
		authUsers[index] = auth.User{Username: user.Name, Password: user.Password}
	}
	return &userState{
		users:         usersCopy,
		authenticator: auth.NewAuthenticator(authUsers),
	}, nil
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	err := h.newConnection(ctx, conn, metadata, onClose)
	N.CloseOnHandshakeFailure(conn, onClose, err)
	if err != nil {
		if E.IsClosedOrCanceled(err) {
			h.logger.DebugContext(ctx, "connection closed: ", err)
		} else {
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
		}
	}
}

func (h *Inbound) newConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) error {
	if h.tlsConfig != nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			return E.Cause(err, "TLS handshake")
		}
		conn = tlsConn
	}
	authenticator := h.userState.Load().authenticator
	reader := std_bufio.NewReader(conn)
	headerBytes, err := reader.Peek(1)
	if err != nil {
		return E.Cause(err, "peek first byte")
	}
	switch headerBytes[0] {
	case socks4.Version, socks5.Version:
		return socks.HandleConnectionEx(ctx, conn, reader, authenticator, adapter.NewUpstreamHandler(metadata, h.newUserConnection, h.streamUserPacketConnection), h.listener, h.udpTimeout, metadata.Source, onClose)
	default:
		return http.HandleConnectionEx(ctx, conn, reader, authenticator, adapter.NewUpstreamHandler(metadata, h.newUserConnection, h.streamUserPacketConnection), metadata.Source, onClose)
	}
}

func (h *Inbound) newUserConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	user, loaded := auth.UserFromContext[string](ctx)
	if !loaded {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
		h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	metadata.User = user
	h.logger.InfoContext(ctx, "[", user, "] inbound connection to ", metadata.Destination)
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) streamUserPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	user, loaded := auth.UserFromContext[string](ctx)
	if !loaded {
		if !metadata.Destination.IsValid() {
			h.logger.InfoContext(ctx, "inbound packet connection")
		} else {
			h.logger.InfoContext(ctx, "inbound packet connection to ", metadata.Destination)
		}
		h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	metadata.User = user
	if !metadata.Destination.IsValid() {
		h.logger.InfoContext(ctx, "[", user, "] inbound packet connection")
	} else {
		h.logger.InfoContext(ctx, "[", user, "] inbound packet connection to ", metadata.Destination)
	}
	h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}
