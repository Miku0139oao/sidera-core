package listener

import (
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/redir"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/sing/common/control"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"github.com/database64128/tfo-go/v2"
)

func (l *Listener) ListenTCP() (net.Listener, error) {
	if l.listenOptions.ProxyProtocolAcceptNoHeader && !l.listenOptions.ProxyProtocol {
		return nil, E.New("proxy_protocol_accept_no_header requires proxy_protocol")
	}
	if l.listenOptions.ProxyProtocol && (l.listenOptions.ProxyProtocolTrustedUpstream == nil || len(*l.listenOptions.ProxyProtocolTrustedUpstream) == 0) {
		return nil, E.New("proxy_protocol requires proxy_protocol_trusted_upstream")
	}
	var err error
	bindAddr := M.SocksaddrFrom(l.listenOptions.Listen.Build(netip.AddrFrom4([4]byte{127, 0, 0, 1})), l.listenOptions.ListenPort)
	var listenConfig net.ListenConfig
	if l.listenOptions.BindInterface != "" {
		listenConfig.Control = control.Append(listenConfig.Control, control.BindToInterface(service.FromContext[adapter.NetworkManager](l.ctx).InterfaceFinder(), l.listenOptions.BindInterface, -1))
	}
	if l.listenOptions.RoutingMark != 0 {
		listenConfig.Control = control.Append(listenConfig.Control, control.RoutingMark(uint32(l.listenOptions.RoutingMark)))
	}
	if l.listenOptions.ReuseAddr {
		listenConfig.Control = control.Append(listenConfig.Control, control.ReuseAddr())
	}
	if l.listenOptions.DisableTCPKeepAlive {
		listenConfig.KeepAlive = -1
		listenConfig.KeepAliveConfig.Enable = false
	} else {
		keepIdle := time.Duration(l.listenOptions.TCPKeepAlive)
		if keepIdle == 0 {
			keepIdle = C.TCPKeepAliveInitial
		}
		keepInterval := time.Duration(l.listenOptions.TCPKeepAliveInterval)
		if keepInterval == 0 {
			keepInterval = C.TCPKeepAliveInterval
		}
		listenConfig.KeepAliveConfig = net.KeepAliveConfig{
			Enable:   true,
			Idle:     keepIdle,
			Interval: keepInterval,
		}
	}
	if l.listenOptions.TCPMultiPath {
		listenConfig.SetMultipathTCP(true)
	}
	if l.tproxy {
		listenConfig.Control = control.Append(listenConfig.Control, func(network, address string, conn syscall.RawConn) error {
			return control.Raw(conn, func(fd uintptr) error {
				return redir.TProxy(fd, !strings.HasSuffix(network, "4"), false)
			})
		})
	}
	tcpListener, err := ListenNetworkNamespace[net.Listener](l.ctx, l.listenOptions.NetNs, func() (net.Listener, error) {
		if l.listenOptions.TCPFastOpen {
			var tfoConfig tfo.ListenConfig
			tfoConfig.ListenConfig = listenConfig
			return tfoConfig.Listen(l.ctx, M.NetworkFromNetAddr(N.NetworkTCP, bindAddr.Addr), bindAddr.String())
		} else {
			return listenConfig.Listen(l.ctx, M.NetworkFromNetAddr(N.NetworkTCP, bindAddr.Addr), bindAddr.String())
		}
	})
	if err != nil {
		return nil, err
	}
	if l.listenOptions.ProxyProtocol {
		l.proxyProtocolTrustedUpstream = make([]netip.Prefix, 0, len(*l.listenOptions.ProxyProtocolTrustedUpstream))
		for _, rawPrefix := range *l.listenOptions.ProxyProtocolTrustedUpstream {
			l.proxyProtocolTrustedUpstream = append(l.proxyProtocolTrustedUpstream, netip.Prefix(rawPrefix))
		}
		tcpListener = &proxyProtocolListener{Listener: tcpListener, owner: l}
		l.logger.Debug("PROXY protocol enabled")
	}
	l.logger.Info("tcp server started at ", tcpListener.Addr())
	l.tcpListener = tcpListener
	return tcpListener, err
}

func (l *Listener) loopTCPIn() {
	tcpListener := l.tcpListener
	for {
		conn, err := tcpListener.Accept()
		if err != nil {
			//nolint:staticcheck
			if netError, isNetError := err.(net.Error); isNetError && netError.Temporary() {
				l.logger.Error(err)
				continue
			}
			if l.shutdown.Load() && E.IsClosed(err) {
				return
			}
			l.tcpListener.Close()
			l.logger.Error("tcp listener closed: ", err)
			continue
		}
		go l.newTCPConnection(conn)
	}
}

func (l *Listener) newTCPConnection(conn net.Conn) {
	if proxyConn, isProxyConn := conn.(*proxyProtocolConn); isProxyConn {
		if err := proxyConn.prepare(); err != nil {
			conn.Close()
			l.logger.Error("process PROXY protocol connection: ", err)
			return
		}
	}
	var metadata adapter.InboundContext
	//nolint:staticcheck
	metadata.InboundDetour = l.listenOptions.Detour
	metadata.Source = M.SocksaddrFromNet(conn.RemoteAddr()).Unwrap()
	metadata.OriginDestination = M.SocksaddrFromNet(conn.LocalAddr()).Unwrap()
	ctx := log.ContextWithNewID(l.ctx)
	l.logger.InfoContext(ctx, "inbound connection from ", metadata.Source)
	l.connHandler.NewConnection(ctx, conn, metadata, nil)
}
