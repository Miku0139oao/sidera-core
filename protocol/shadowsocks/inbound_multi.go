package shadowsocks

import (
	"context"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/Miku0139oao/sidera-core/common/mux"
	"github.com/Miku0139oao/sidera-core/common/uot"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

var (
	_ adapter.TCPInjectableInbound = (*MultiInbound)(nil)
	_ adapter.ManagedSSMServer     = (*MultiInbound)(nil)
	_ adapter.ManagedUserService   = (*MultiInbound)(nil)
)

type multiInboundUser struct {
	name        string
	displayName string
}

type multiInboundUserContextKey struct{}

type multiInboundUserContext struct {
	userNameMap map[string]multiInboundUser
	readGuard   *multiInboundServiceReadGuard
}

type multiInboundServiceReadGuard struct {
	once    sync.Once
	release func()
}

func (g *multiInboundServiceReadGuard) Release() {
	if g != nil {
		g.once.Do(g.release)
	}
}

type MultiInbound struct {
	inbound.Adapter
	ctx           context.Context
	router        adapter.ConnectionRouterEx
	logger        logger.ContextLogger
	listener      *listener.Listener
	serviceAccess sync.RWMutex
	service       shadowsocks.MultiService[string]
	userAccess    sync.RWMutex
	users         []adapter.ManagedUser
	userNameMap   map[string]multiInboundUser
	passwordBytes int
	tracker       adapter.SSMTracker
}

func newMultiInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (*MultiInbound, error) {
	inbound := &MultiInbound{
		Adapter: inbound.NewAdapter(C.TypeShadowsocks, tag),
		ctx:     ctx,
		router:  uot.NewRouter(router, logger),
		logger:  logger,
	}
	var err error
	inbound.router, err = mux.NewRouterWithOptions(inbound.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}
	var service shadowsocks.MultiService[string]
	if common.Contains(shadowaead_2022.List, options.Method) {
		if options.Method == "2022-blake3-aes-128-gcm" {
			inbound.passwordBytes = 16
		} else {
			inbound.passwordBytes = 32
		}
		service, err = shadowaead_2022.NewMultiServiceWithPassword[string](
			options.Method,
			options.Password,
			int64(udpTimeout.Seconds()),
			adapter.NewLegacyUpstreamHandler(adapter.InboundContext{}, inbound.newConnection, inbound.newPacketConnection, inbound),
			ntp.TimeFuncFromContext(ctx),
		)
	} else if common.Contains(shadowaead.List, options.Method) {
		service, err = shadowaead.NewMultiService[string](
			options.Method,
			int64(udpTimeout.Seconds()),
			adapter.NewLegacyUpstreamHandler(adapter.InboundContext{}, inbound.newConnection, inbound.newPacketConnection, inbound),
		)
	} else {
		return nil, E.New("unsupported method: " + options.Method)
	}
	if err != nil {
		return nil, err
	}
	inbound.service = service
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
	inbound.listener = listener.New(listener.Options{
		Context:                  ctx,
		Logger:                   logger,
		Network:                  options.Network.Build(),
		Listen:                   options.ListenOptions,
		ConnectionHandler:        inbound,
		PacketHandler:            inbound,
		ThreadUnsafePacketWriter: true,
	})
	return inbound, err
}

func (h *MultiInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return h.listener.Start()
}

func (h *MultiInbound) Close() error {
	return h.listener.Close()
}

func (h *MultiInbound) SetTracker(tracker adapter.SSMTracker) {
	h.tracker = tracker
}

func (h *MultiInbound) UpdateUsers(users []string, uPSKs []string) error {
	if len(users) != len(uPSKs) {
		return E.New("shadowsocks: user/password count mismatch")
	}
	managedUsers := make([]adapter.ManagedUser, len(users))
	for index, user := range users {
		managedUsers[index] = adapter.ManagedUser{
			Name:     user,
			Password: uPSKs[index],
		}
	}
	return h.UpdateManagedUsers(managedUsers)
}

func (h *MultiInbound) ManagedUserSchema() adapter.ManagedUserSchema {
	schema := adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialPassword}
	if h.passwordBytes > 0 {
		schema.PasswordEncoding = adapter.ManagedUserPasswordBase64
		schema.PasswordBytes = h.passwordBytes
	}
	return schema
}

func (h *MultiInbound) ManagedUsers() []adapter.ManagedUser {
	h.userAccess.RLock()
	defer h.userAccess.RUnlock()
	return append([]adapter.ManagedUser(nil), h.users...)
}

func (h *MultiInbound) UpdateManagedUsers(users []adapter.ManagedUser) error {
	userList := make([]string, len(users))
	passwordList := make([]string, len(users))
	managedUsers := make([]adapter.ManagedUser, len(users))
	userNameMap := make(map[string]multiInboundUser, len(users))
	for index, user := range users {
		if user.Password == "" {
			return shadowsocks.ErrMissingPassword
		}
		userList[index] = user.Password
		passwordList[index] = user.Password
		managedUsers[index] = adapter.ManagedUser{
			Name:     user.Name,
			Password: user.Password,
		}
		displayName := user.Name
		if displayName == "" {
			displayName = F.ToString(index)
		}
		userNameMap[user.Password] = multiInboundUser{
			name:        user.Name,
			displayName: displayName,
		}
	}

	h.serviceAccess.Lock()
	defer h.serviceAccess.Unlock()
	if err := h.service.UpdateUsersWithPasswords(userList, passwordList); err != nil {
		return err
	}
	h.userAccess.Lock()
	h.users = managedUsers
	h.userNameMap = userNameMap
	h.userAccess.Unlock()
	return nil
}

//nolint:staticcheck
func (h *MultiInbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	h.serviceAccess.RLock()
	h.userAccess.RLock()
	userNameMap := h.userNameMap
	h.userAccess.RUnlock()
	readGuard := &multiInboundServiceReadGuard{release: h.serviceAccess.RUnlock}
	ctx = context.WithValue(ctx, multiInboundUserContextKey{}, &multiInboundUserContext{
		userNameMap: userNameMap,
		readGuard:   readGuard,
	})
	err := h.service.NewConnection(ctx, conn, adapter.UpstreamMetadata(metadata))
	readGuard.Release()
	N.CloseOnHandshakeFailure(conn, onClose, err)
	if err != nil {
		if E.IsClosedOrCanceled(err) {
			h.logger.DebugContext(ctx, "connection closed: ", err)
		} else {
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
		}
	}
}

//nolint:staticcheck
func (h *MultiInbound) NewPacket(buffer *buf.Buffer, source M.Socksaddr) {
	h.serviceAccess.RLock()
	h.userAccess.RLock()
	userNameMap := h.userNameMap
	h.userAccess.RUnlock()
	ctx := context.WithValue(h.ctx, multiInboundUserContextKey{}, &multiInboundUserContext{userNameMap: userNameMap})
	err := h.service.NewPacket(ctx, &stubPacketConn{h.listener.PacketWriter()}, buffer, M.Metadata{Source: source})
	h.serviceAccess.RUnlock()
	if err != nil {
		h.logger.Error(E.Cause(err, "process packet from ", source))
	}
}

func (h *MultiInbound) newConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	user, loaded := h.userFromContext(ctx)
	if !loaded {
		return os.ErrInvalid
	}
	if user.name != "" {
		metadata.User = user.name
	}
	h.logger.InfoContext(ctx, "[", user.displayName, "] inbound connection to ", metadata.Destination)
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	if h.tracker != nil {
		conn = h.tracker.TrackConnection(conn, metadata)
	}
	return h.router.RouteConnection(ctx, conn, metadata)
}

func (h *MultiInbound) newPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	user, loaded := h.userFromContext(ctx)
	if !loaded {
		return os.ErrInvalid
	}
	if user.name != "" {
		metadata.User = user.name
	}
	ctx = log.ContextWithNewID(ctx)
	h.logger.InfoContext(ctx, "[", user.displayName, "] inbound packet connection from ", metadata.Source)
	h.logger.InfoContext(ctx, "[", user.displayName, "] inbound packet connection to ", metadata.Destination)
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	if h.tracker != nil {
		conn = h.tracker.TrackPacketConnection(conn, metadata)
	}
	return h.router.RoutePacketConnection(ctx, conn, metadata)
}

func (h *MultiInbound) userFromContext(ctx context.Context) (multiInboundUser, bool) {
	userContext, _ := ctx.Value(multiInboundUserContextKey{}).(*multiInboundUserContext)
	if userContext != nil {
		defer userContext.readGuard.Release()
	}
	credential, loaded := auth.UserFromContext[string](ctx)
	if !loaded {
		return multiInboundUser{}, false
	}
	if userContext != nil {
		user, loaded := userContext.userNameMap[credential]
		return user, loaded
	}
	h.userAccess.RLock()
	user, loaded := h.userNameMap[credential]
	h.userAccess.RUnlock()
	return user, loaded
}

//nolint:staticcheck
func (h *MultiInbound) NewError(ctx context.Context, err error) {
	NewError(h.logger, ctx, err)
}
