package anytls

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"

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
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.AnyTLSInboundOptions](registry, C.TypeAnyTLS, NewInbound)
}

var (
	_ adapter.TCPInjectableInbound = (*Inbound)(nil)
	_ adapter.ManagedUserService   = (*Inbound)(nil)
)

type trackedConnection struct {
	owner uint64
	done  chan struct{}
}

type Inbound struct {
	inbound.Adapter
	tlsConfig        tls.ServerConfig
	router           adapter.ConnectionRouterEx
	logger           logger.ContextLogger
	listener         *listener.Listener
	serviceAccess    sync.RWMutex
	service          *anytls.Service
	users            []adapter.ManagedUser
	paddingScheme    []byte
	connectionAccess sync.Mutex
	connections      map[net.Conn]*trackedConnection
	closeDone        chan struct{}
	closeOwner       uint64
	closeErr         error
	closing          bool
	closed           bool
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AnyTLSInboundOptions) (adapter.Inbound, error) {
	inbound := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeAnyTLS, tag),
		router:  uot.NewRouter(router, logger),
		logger:  logger,
	}

	if options.TLS != nil && options.TLS.Enabled {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
		inbound.tlsConfig = tlsConfig
	}

	paddingScheme := padding.DefaultPaddingScheme
	if len(options.PaddingScheme) > 0 {
		paddingScheme = []byte(strings.Join(options.PaddingScheme, "\n"))
	}

	inbound.paddingScheme = append([]byte(nil), paddingScheme...)
	users := make([]adapter.ManagedUser, len(options.Users))
	for index, user := range options.Users {
		users[index] = adapter.ManagedUser{Name: user.Name, Password: user.Password}
	}
	service, users, err := inbound.buildService(users)
	if err != nil {
		return nil, err
	}
	inbound.service = service
	inbound.users = users
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
	if h.tlsConfig != nil {
		err := h.tlsConfig.Start()
		if err != nil {
			return err
		}
	}
	return h.listener.Start()
}

func (h *Inbound) Close() (err error) {
	owner := anyTLSGoroutineID()
	h.connectionAccess.Lock()
	if h.closed {
		closeErr := h.closeErr
		h.connectionAccess.Unlock()
		return E.Errors(os.ErrClosed, closeErr)
	}
	if h.closing {
		if owner != 0 && owner == h.closeOwner {
			h.connectionAccess.Unlock()
			return os.ErrClosed
		}
		closeDone := h.closeDone
		h.connectionAccess.Unlock()
		<-closeDone
		h.connectionAccess.Lock()
		closeErr := h.closeErr
		h.connectionAccess.Unlock()
		return E.Errors(os.ErrClosed, closeErr)
	}
	h.closing = true
	h.closeOwner = owner
	h.closeDone = make(chan struct{})
	calledFromSession := false
	connections := make([]net.Conn, 0, len(h.connections))
	connectionStates := make([]*trackedConnection, 0, len(h.connections))
	for conn, connectionState := range h.connections {
		connections = append(connections, conn)
		connectionStates = append(connectionStates, connectionState)
		calledFromSession = calledFromSession || owner != 0 && owner == connectionState.owner
	}
	h.connectionAccess.Unlock()

	defer func() {
		panicValue := recover()
		h.connectionAccess.Lock()
		h.closeErr = err
		h.closing = false
		h.closed = true
		h.closeOwner = 0
		close(h.closeDone)
		h.connectionAccess.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	err = common.Close(h.listener)
	for _, conn := range connections {
		err = E.Errors(err, conn.Close())
	}
	for _, connectionState := range connectionStates {
		if calledFromSession && owner == connectionState.owner {
			continue
		}
		<-connectionState.done
	}
	err = E.Errors(err, common.Close(h.tlsConfig))
	return err
}

func (h *Inbound) ManagedUserSchema() adapter.ManagedUserSchema {
	return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialPassword}
}

func (h *Inbound) ManagedUsers() []adapter.ManagedUser {
	h.serviceAccess.RLock()
	users := append([]adapter.ManagedUser(nil), h.users...)
	h.serviceAccess.RUnlock()
	return users
}

func (h *Inbound) UpdateManagedUsers(users []adapter.ManagedUser) error {
	service, usersCopy, err := h.buildService(users)
	if err != nil {
		return err
	}
	h.connectionAccess.Lock()
	if h.closing || h.closed {
		h.connectionAccess.Unlock()
		return os.ErrClosed
	}
	h.serviceAccess.Lock()
	h.service = service
	h.users = usersCopy
	h.serviceAccess.Unlock()
	h.connectionAccess.Unlock()
	return nil
}

func (h *Inbound) buildService(users []adapter.ManagedUser) (*anytls.Service, []adapter.ManagedUser, error) {
	usersCopy, serviceUsers, err := buildUsers(users)
	if err != nil {
		return nil, nil, err
	}
	service, err := anytls.NewService(anytls.ServiceConfig{
		Users:         serviceUsers,
		PaddingScheme: h.paddingScheme,
		Handler:       (*inboundHandler)(h),
		Logger:        h.logger,
	})
	if err != nil {
		return nil, nil, err
	}
	return service, usersCopy, nil
}

func buildUsers(users []adapter.ManagedUser) ([]adapter.ManagedUser, []anytls.User, error) {
	names := make(map[string]struct{}, len(users))
	usersCopy := make([]adapter.ManagedUser, len(users))
	serviceUsers := make([]anytls.User, len(users))
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
		serviceUsers[index] = anytls.User{Name: user.Name, Password: user.Password}
	}
	return usersCopy, serviceUsers, nil
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	trackedConn := conn
	if !h.trackConnection(trackedConn) {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrClosed)
		return
	}
	defer h.untrackConnection(trackedConn)
	if h.tlsConfig != nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			N.CloseOnHandshakeFailure(conn, onClose, err)
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": TLS handshake"))
			return
		}
		conn = tlsConn
	}
	h.serviceAccess.RLock()
	service := h.service
	h.serviceAccess.RUnlock()
	err := service.NewConnection(adapter.WithContext(ctx, &metadata), conn, metadata.Source, onClose)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
	}
}

func (h *Inbound) trackConnection(conn net.Conn) bool {
	h.connectionAccess.Lock()
	defer h.connectionAccess.Unlock()
	if h.closing || h.closed {
		return false
	}
	if h.connections == nil {
		h.connections = make(map[net.Conn]*trackedConnection)
	}
	h.connections[conn] = &trackedConnection{owner: anyTLSGoroutineID(), done: make(chan struct{})}
	return true
}

func (h *Inbound) untrackConnection(conn net.Conn) {
	h.connectionAccess.Lock()
	connectionState := h.connections[conn]
	delete(h.connections, conn)
	h.connectionAccess.Unlock()
	if connectionState != nil {
		close(connectionState.done)
	}
}

func anyTLSGoroutineID() uint64 {
	var buffer [64]byte
	length := runtime.Stack(buffer[:], false)
	const prefix = "goroutine "
	if length <= len(prefix) {
		return 0
	}
	var id uint64
	for index := len(prefix); index < length; index++ {
		value := buffer[index]
		if value < '0' || value > '9' {
			break
		}
		id = id*10 + uint64(value-'0')
	}
	return id
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
	metadata.Destination = destination.Unwrap()
	if userName, _ := auth.UserFromContext[string](ctx); userName != "" {
		metadata.User = userName
		h.logger.InfoContext(ctx, "[", userName, "] inbound connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}
