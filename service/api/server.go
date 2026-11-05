package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	boxService "github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/Miku0139oao/sidera-core/common/tls"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/daemon"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.APIServiceOptions](registry, C.TypeAPI, NewService)
}

// ProducerQuiescenceLifecycle splits API admission shutdown from the final
// traffic checkpoint. Box must stop admission, quiesce inbound traffic
// producers, and only then call CloseAfterProducerQuiescence.
type ProducerQuiescenceLifecycle interface {
	StopAdmission() error
	CloseAfterProducerQuiescence() error
}

var _ ProducerQuiescenceLifecycle = (*Service)(nil)

type Service struct {
	boxService.Adapter
	ctx             context.Context
	cancel          context.CancelFunc
	logger          log.ContextLogger
	options         option.APIServiceOptions
	listener        *listener.Listener
	tlsConfig       tls.ServerConfig
	startedService  *daemon.StartedService
	grpcServer      *grpc.Server
	httpServer      *http.Server
	tcpListener     net.Listener
	dashboard       *dashboard
	admin           *adminAPI
	runtimeRollback func() error
	serveWorkers    sync.WaitGroup
	stopOnce        sync.Once
	stopErr         error
	finalizeOnce    sync.Once
	finalizeErr     error
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.APIServiceOptions) (adapter.Service, error) {
	if err := validateDashboardExposure(options); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{
		Adapter: boxService.NewAdapter(C.TypeAPI, tag),
		ctx:     ctx,
		cancel:  cancel,
		logger:  logger,
		options: options,
		listener: listener.New(listener.Options{
			Context: ctx,
			Logger:  logger,
			Network: []string{N.NetworkTCP},
			Listen:  options.ListenOptions,
		}),
	}
	if options.TLS != nil {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			cancel()
			return nil, err
		}
		s.tlsConfig = tlsConfig
	}
	if options.Dashboard != nil && options.Dashboard.Enabled {
		s.dashboard = newDashboard(ctx, logger, *options.Dashboard)
		admin, err := newAdminAPI(ctx, logger, options.Secret, options.Dashboard.DataPath, options.Dashboard.PublicBaseURL, options.Dashboard.AppliedServerRevisions, options.Dashboard.ProcessSignalReload)
		if err != nil {
			cancel()
			return nil, E.Cause(err, "initialize dashboard management")
		}
		admin.secure = options.TLS != nil && options.TLS.Enabled
		s.admin = admin
	}
	return s, nil
}

func validateDashboardExposure(options option.APIServiceOptions) error {
	if options.Dashboard == nil || !options.Dashboard.Enabled {
		return nil
	}
	if strings.TrimSpace(options.Secret) == "" {
		return E.New("dashboard API requires a secret")
	}
	if err := validateSubscriptionBaseURL(options.Dashboard.PublicBaseURL); err != nil {
		return err
	}
	listenAddress := options.Listen.Build(netip.AddrFrom4([4]byte{127, 0, 0, 1}))
	if listenAddress.IsLoopback() {
		return nil
	}
	if options.TLS == nil || !options.TLS.Enabled {
		return E.New("dashboard API exposed on a non-loopback address requires TLS")
	}
	return nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStarted {
		return nil
	}
	s.startedService = daemon.NewAttachedService(s.ctx)
	s.grpcServer = daemon.NewServer(s.startedService, s.options.Secret)
	if s.dashboard != nil {
		err := s.dashboard.start()
		if err != nil {
			return E.Cause(err, "start dashboard")
		}
	}
	s.httpServer = &http.Server{
		Handler: h2c.NewHandler(newHTTPHandler(s.logger, s.grpcServer, s.options, s.dashboard, s.admin), new(http2.Server)),
		BaseContext: func(net.Listener) context.Context {
			return s.ctx
		},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	if s.tlsConfig != nil {
		err := s.tlsConfig.Start()
		if err != nil {
			return E.Cause(err, "create TLS config")
		}
		if !common.Contains(s.tlsConfig.NextProtos(), http2.NextProtoTLS) {
			s.tlsConfig.SetNextProtos(append([]string{http2.NextProtoTLS}, s.tlsConfig.NextProtos()...))
		}
		if !common.Contains(s.tlsConfig.NextProtos(), "http/1.1") {
			s.tlsConfig.SetNextProtos(append(s.tlsConfig.NextProtos(), "http/1.1"))
		}
	}
	tcpListener, err := s.listener.ListenTCP()
	if err != nil {
		return err
	}
	if s.tlsConfig != nil {
		tcpListener = aTLS.NewListener(tcpListener, s.tlsConfig)
	}
	s.tcpListener = tcpListener
	return nil
}

// CommitRuntime persists state that is valid only after the complete Box has
// started successfully.
func (s *Service) CommitRuntime() error {
	if s.admin != nil {
		rollback, err := s.admin.markServerProfilesApplied()
		if err != nil {
			return err
		}
		s.runtimeRollback = rollback
	}
	return nil
}

func (s *Service) RollbackRuntime() error {
	rollback := s.runtimeRollback
	s.runtimeRollback = nil
	if rollback != nil {
		return rollback()
	}
	return nil
}

func (s *Service) ValidateRuntimeActivation() error {
	if s.tcpListener == nil || s.httpServer == nil {
		return E.New("API listener is not ready")
	}
	return nil
}

func (s *Service) ActivateRuntime() {
	s.runtimeRollback = nil
	if s.admin != nil {
		if err := s.admin.start(); err != nil {
			s.logger.Error("start dashboard management: ", err)
		}
	}
	tcpListener := s.tcpListener
	s.tcpListener = nil
	s.serveWorkers.Go(func() {
		serveErr := s.httpServer.Serve(tcpListener)
		if serveErr != nil && s.ctx.Err() == nil {
			s.logger.Error("serve error: ", serveErr)
		}
	})
}

func (s *Service) StopAdmission() error {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.httpServer != nil {
			s.stopErr = errors.Join(s.stopErr, s.httpServer.Close())
		}
		if s.grpcServer != nil {
			s.grpcServer.Stop()
		}
		if s.admin != nil {
			s.admin.stopAdmission()
		}
		if s.dashboard != nil {
			s.stopErr = errors.Join(s.stopErr, s.dashboard.close())
		}
		if s.startedService != nil {
			s.startedService.Close()
		}
		s.serveWorkers.Wait()
	})
	return filterCloseError(s.stopErr)
}

func (s *Service) CloseAfterProducerQuiescence() error {
	stopErr := s.StopAdmission()
	s.finalizeOnce.Do(func() {
		if s.admin != nil {
			s.finalizeErr = errors.Join(s.finalizeErr, s.admin.finalizeTrafficAccounting())
		}
		s.finalizeErr = errors.Join(s.finalizeErr, filterCloseError(common.Close(
			common.PtrOrNil(s.listener),
			s.tlsConfig,
		)))
	})
	return errors.Join(stopErr, s.finalizeErr)
}

func (s *Service) Close() error {
	return s.CloseAfterProducerQuiescence()
}

func filterCloseError(err error) error {
	if err == nil {
		return nil
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		var filtered []error
		for _, e := range joined.Unwrap() {
			if e == nil || errors.Is(e, net.ErrClosed) {
				continue
			}
			filtered = append(filtered, e)
		}
		if len(filtered) == 0 {
			return nil
		}
		return errors.Join(filtered...)
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
