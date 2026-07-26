package vless

import (
	"context"
	"net"
	"os"
	"sync"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/Miku0139oao/sidera-core/common/mux"
	"github.com/Miku0139oao/sidera-core/common/tls"
	"github.com/Miku0139oao/sidera-core/common/uot"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/Miku0139oao/sidera-core/protocol/vless/xrayencryption"
	"github.com/Miku0139oao/sidera-core/transport/v2ray"
	"github.com/sagernet/sing-vmess/packetaddr"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.VLESSInboundOptions](registry, C.TypeVLESS, NewInbound)
}

var (
	_ adapter.TCPInjectableInbound = (*Inbound)(nil)
	_ adapter.ManagedUserService   = (*Inbound)(nil)
)

type Inbound struct {
	inbound.Adapter
	ctx        context.Context
	router     adapter.ConnectionRouterEx
	logger     logger.ContextLogger
	listener   *listener.Listener
	service    *vless.Service[string]
	decryption *xrayencryption.ServerInstance
	tlsConfig  tls.ServerConfig
	transport  adapter.V2RayServerTransport

	serviceAccess sync.RWMutex
	usersAccess   sync.RWMutex
	users         map[string]option.VLESSUser
	managedUsers  []adapter.ManagedUser
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.VLESSInboundOptions) (adapter.Inbound, error) {
	inbound := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeVLESS, tag),
		ctx:     ctx,
		router:  uot.NewRouter(router, logger),
		logger:  logger,
	}
	var err error
	if options.Decryption != "" {
		inbound.decryption, err = xrayencryption.ParseDecryption(options.Decryption)
		if err != nil {
			return nil, E.Cause(err, "create VLESS decryption")
		}
	}
	inbound.router, err = mux.NewRouterWithOptions(inbound.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	service := vless.NewService[string](logger, adapter.NewUpstreamContextHandler(inbound.newConnectionEx, inbound.newPacketConnectionEx))
	inbound.service = service
	managedUsers := make([]adapter.ManagedUser, len(options.Users))
	for index, user := range options.Users {
		managedUsers[index] = adapter.ManagedUser{Name: user.Name, UUID: user.UUID, Flow: user.Flow}
	}
	err = inbound.UpdateManagedUsers(managedUsers)
	if err != nil {
		return nil, err
	}
	if options.TLS != nil {
		inbound.tlsConfig, err = tls.NewServerWithOptions(tls.ServerOptions{
			Context: ctx,
			Logger:  logger,
			Options: common.PtrValueOrDefault(options.TLS),
			KTLSCompatible: common.PtrValueOrDefault(options.Transport).Type == "" &&
				!common.PtrValueOrDefault(options.Multiplex).Enabled &&
				common.All(options.Users, func(it option.VLESSUser) bool {
					return it.Flow == ""
				}),
		})
		if err != nil {
			return nil, err
		}
	}
	if options.Transport != nil {
		inbound.transport, err = v2ray.NewServerTransport(ctx, logger, common.PtrValueOrDefault(options.Transport), inbound.tlsConfig, (*inboundTransportHandler)(inbound))
		if err != nil {
			return nil, E.Cause(err, "create server transport: ", options.Transport.Type)
		}
	}
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
	return adapter.ManagedUserSchema{
		Credential: adapter.ManagedUserCredentialUUID,
		Flow:       true,
	}
}

func (h *Inbound) ManagedUsers() []adapter.ManagedUser {
	h.usersAccess.RLock()
	defer h.usersAccess.RUnlock()
	return append([]adapter.ManagedUser(nil), h.managedUsers...)
}

func (h *Inbound) UpdateManagedUsers(users []adapter.ManagedUser) error {
	userList := make([]string, len(users))
	uuidList := make([]string, len(users))
	flowList := make([]string, len(users))
	userMap := make(map[string]option.VLESSUser, len(users))
	managedUsers := make([]adapter.ManagedUser, len(users))
	for index, user := range users {
		if user.UUID == "" {
			return E.New("missing uuid for user ", index)
		}
		if h.decryption != nil && user.Flow == vless.FlowVision && !xrayEncryptionVisionSupported() {
			return E.New("VLESS encryption with Vision requires the badlinkname build tag")
		}
		protocolUser := option.VLESSUser{Name: user.Name, UUID: user.UUID, Flow: user.Flow}
		userList[index] = user.UUID
		uuidList[index] = user.UUID
		flowList[index] = user.Flow
		userMap[user.UUID] = protocolUser
		managedUsers[index] = adapter.ManagedUser{Name: user.Name, UUID: user.UUID, Flow: user.Flow}
	}

	h.serviceAccess.Lock()
	h.usersAccess.Lock()
	h.service.UpdateUsers(userList, uuidList, flowList)
	h.users = userMap
	h.managedUsers = managedUsers
	h.usersAccess.Unlock()
	h.serviceAccess.Unlock()
	return nil
}

func (h *Inbound) userName(userID string) string {
	h.usersAccess.RLock()
	user := h.users[userID]
	h.usersAccess.RUnlock()
	return user.Name
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
	if h.transport == nil {
		return h.listener.Start()
	}
	if common.Contains(h.transport.Network(), N.NetworkTCP) {
		tcpListener, err := h.listener.ListenTCP()
		if err != nil {
			return err
		}
		go func() {
			sErr := h.transport.Serve(tcpListener)
			if sErr != nil && !E.IsClosed(sErr) {
				h.logger.Error("transport serve error: ", sErr)
			}
		}()
	}
	if common.Contains(h.transport.Network(), N.NetworkUDP) {
		udpConn, err := h.listener.ListenUDP()
		if err != nil {
			return err
		}
		go func() {
			sErr := h.transport.ServePacket(udpConn)
			if sErr != nil && !E.IsClosed(sErr) {
				h.logger.Error("transport serve error: ", sErr)
			}
		}()
	}
	return nil
}

func (h *Inbound) Close() error {
	return common.Close(
		h.service,
		common.PtrOrNil(h.decryption),
		h.listener,
		h.tlsConfig,
		h.transport,
	)
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if h.tlsConfig != nil && h.transport == nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			N.CloseOnHandshakeFailure(conn, onClose, err)
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": TLS handshake"))
			return
		}
		conn = tlsConn
	}
	if h.decryption != nil {
		decryptedConn, err := h.decryption.Handshake(conn, nil)
		if err != nil {
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": VLESS decryption handshake"))
			N.CloseOnHandshakeFailure(conn, onClose, err)
			return
		}
		conn = decryptedConn
	}
	h.serviceAccess.RLock()
	err := h.service.NewConnection(adapter.WithContext(ctx, &metadata), conn, metadata.Source, onClose)
	h.serviceAccess.RUnlock()
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
	}
}

func (h *Inbound) newConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	userID, loaded := auth.UserFromContext[string](ctx)
	if !loaded {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
		return
	}
	user := h.userName(userID)
	if user == "" {
		user = userID
	} else {
		metadata.User = user
	}
	h.logger.InfoContext(ctx, "[", user, "] inbound connection to ", metadata.Destination)
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) newPacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	userID, loaded := auth.UserFromContext[string](ctx)
	if !loaded {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
		return
	}
	user := h.userName(userID)
	if user == "" {
		user = userID
	} else {
		metadata.User = user
	}
	if metadata.Destination.Fqdn == packetaddr.SeqPacketMagicAddress {
		metadata.Destination = M.Socksaddr{}
		conn = packetaddr.NewConn(bufio.NewNetPacketConn(conn), metadata.Destination)
		h.logger.InfoContext(ctx, "[", user, "] inbound packet addr connection")
	} else {
		h.logger.InfoContext(ctx, "[", user, "] inbound packet connection to ", metadata.Destination)
	}
	h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

var _ adapter.V2RayServerTransportHandler = (*inboundTransportHandler)(nil)

type inboundTransportHandler Inbound

func (h *inboundTransportHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Source = source
	metadata.Destination = destination
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	h.logger.InfoContext(ctx, "inbound connection from ", metadata.Source)
	(*Inbound)(h).NewConnection(ctx, conn, metadata, onClose)
}
