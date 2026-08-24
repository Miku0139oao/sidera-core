package box

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	boxCertificate "github.com/Miku0139oao/sidera-core/adapter/certificate"
	"github.com/Miku0139oao/sidera-core/adapter/endpoint"
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/adapter/outbound"
	boxService "github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/common/certificate"
	"github.com/Miku0139oao/sidera-core/common/dashboardstore"
	"github.com/Miku0139oao/sidera-core/common/dialer"
	"github.com/Miku0139oao/sidera-core/common/httpclient"
	"github.com/Miku0139oao/sidera-core/common/netns"
	"github.com/Miku0139oao/sidera-core/common/taskmonitor"
	"github.com/Miku0139oao/sidera-core/common/tls"
	"github.com/Miku0139oao/sidera-core/common/trafficcontrol"
	"github.com/Miku0139oao/sidera-core/common/urltest"
	"github.com/Miku0139oao/sidera-core/common/validation"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/experimental"
	"github.com/Miku0139oao/sidera-core/experimental/cachefile"
	"github.com/Miku0139oao/sidera-core/experimental/deprecated"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/Miku0139oao/sidera-core/protocol/direct"
	"github.com/Miku0139oao/sidera-core/route"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

var _ adapter.SimpleLifecycle = (*Box)(nil)

type Box struct {
	createdAt           time.Time
	debugOptions        option.DebugOptions
	debugHTTPServer     *http.Server
	logFactory          log.Factory
	logger              log.ContextLogger
	network             *route.NetworkManager
	endpoint            *endpoint.Manager
	inbound             *inbound.Manager
	outbound            *outbound.Manager
	service             *boxService.Manager
	certificateProvider *boxCertificate.Manager
	dnsTransport        *dns.TransportManager
	dnsRouter           *dns.Router
	connection          *route.ConnectionManager
	router              *route.Router
	httpClientService   adapter.LifecycleService
	internalService     []adapter.LifecycleService
	done                chan struct{}
	closeAccess         sync.Mutex
	closeErr            error
	closing             bool
	closed              bool
	beforeRuntimeCommit func() (func() error, error)
}

type Options struct {
	option.Options
	Context                    context.Context
	PlatformLogWriter          log.PlatformWriter
	NetworkNamespaceHolderArgs []string
	ValidationOnly             bool
	BeforeRuntimeCommit        func() (func() error, error)
}

func Context(
	ctx context.Context,
	inboundRegistry adapter.InboundRegistry,
	outboundRegistry adapter.OutboundRegistry,
	endpointRegistry adapter.EndpointRegistry,
	dnsTransportRegistry adapter.DNSTransportRegistry,
	serviceRegistry adapter.ServiceRegistry,
	certificateProviderRegistry adapter.CertificateProviderRegistry,
) context.Context {
	if service.FromContext[option.InboundOptionsRegistry](ctx) == nil ||
		service.FromContext[adapter.InboundRegistry](ctx) == nil {
		ctx = service.ContextWith[option.InboundOptionsRegistry](ctx, inboundRegistry)
		ctx = service.ContextWith[adapter.InboundRegistry](ctx, inboundRegistry)
	}
	if service.FromContext[option.OutboundOptionsRegistry](ctx) == nil ||
		service.FromContext[adapter.OutboundRegistry](ctx) == nil {
		ctx = service.ContextWith[option.OutboundOptionsRegistry](ctx, outboundRegistry)
		ctx = service.ContextWith[adapter.OutboundRegistry](ctx, outboundRegistry)
	}
	if service.FromContext[option.EndpointOptionsRegistry](ctx) == nil ||
		service.FromContext[adapter.EndpointRegistry](ctx) == nil {
		ctx = service.ContextWith[option.EndpointOptionsRegistry](ctx, endpointRegistry)
		ctx = service.ContextWith[adapter.EndpointRegistry](ctx, endpointRegistry)
	}
	if service.FromContext[adapter.DNSTransportRegistry](ctx) == nil {
		ctx = service.ContextWith[option.DNSTransportOptionsRegistry](ctx, dnsTransportRegistry)
		ctx = service.ContextWith[adapter.DNSTransportRegistry](ctx, dnsTransportRegistry)
	}
	if service.FromContext[adapter.ServiceRegistry](ctx) == nil {
		ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, serviceRegistry)
		ctx = service.ContextWith[adapter.ServiceRegistry](ctx, serviceRegistry)
	}
	if service.FromContext[adapter.CertificateProviderRegistry](ctx) == nil {
		ctx = service.ContextWith[option.CertificateProviderOptionsRegistry](ctx, certificateProviderRegistry)
		ctx = service.ContextWith[adapter.CertificateProviderRegistry](ctx, certificateProviderRegistry)
	}
	return ctx
}

func New(options Options) (result *Box, resultErr error) {
	createdAt := time.Now()
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if options.ValidationOnly {
		ctx = validation.Context(ctx)
	}
	ctx = service.ContextWithDefaultRegistry(ctx)

	endpointRegistry := service.FromContext[adapter.EndpointRegistry](ctx)
	inboundRegistry := service.FromContext[adapter.InboundRegistry](ctx)
	outboundRegistry := service.FromContext[adapter.OutboundRegistry](ctx)
	dnsTransportRegistry := service.FromContext[adapter.DNSTransportRegistry](ctx)
	serviceRegistry := service.FromContext[adapter.ServiceRegistry](ctx)
	certificateProviderRegistry := service.FromContext[adapter.CertificateProviderRegistry](ctx)

	if endpointRegistry == nil {
		return nil, E.New("missing endpoint registry in context")
	}
	if inboundRegistry == nil {
		return nil, E.New("missing inbound registry in context")
	}
	if outboundRegistry == nil {
		return nil, E.New("missing outbound registry in context")
	}
	if dnsTransportRegistry == nil {
		return nil, E.New("missing DNS transport registry in context")
	}
	if serviceRegistry == nil {
		return nil, E.New("missing service registry in context")
	}
	if certificateProviderRegistry == nil {
		return nil, E.New("missing certificate provider registry in context")
	}
	options.Options = cloneDashboardOptions(options.Options)
	if err := dashboardstore.MergeProfiles(ctx, &options.Options); err != nil {
		return nil, E.Cause(err, "load dashboard server profiles")
	}
	if err := validateDashboardServices(options.Services); err != nil {
		return nil, err
	}

	ctx = pause.WithDefaultManager(ctx)
	experimentalOptions := common.PtrValueOrDefault(options.Experimental)
	debugOptions := common.PtrValueOrDefault(experimentalOptions.Debug)
	err := checkDebugOptions(debugOptions)
	if err != nil {
		return nil, err
	}
	var needCacheFile bool
	var needClashAPI bool
	var needV2RayAPI bool
	if experimentalOptions.CacheFile != nil && experimentalOptions.CacheFile.Enabled || options.PlatformLogWriter != nil {
		needCacheFile = true
	}
	if experimentalOptions.ClashAPI != nil || options.PlatformLogWriter != nil {
		needClashAPI = true
	}
	if experimentalOptions.V2RayAPI != nil && (experimentalOptions.V2RayAPI.Listen != "" || experimentalOptions.V2RayAPI.Metrics != nil && experimentalOptions.V2RayAPI.Metrics.Listen != "") {
		needV2RayAPI = true
	}
	needAPIService := common.Any(options.Services, func(it option.Service) bool {
		return it.Type == C.TypeAPI
	})
	if service.PtrFromContext[urltest.HistoryStorage](ctx) == nil {
		ctx = service.ContextWithPtr(ctx, urltest.NewHistoryStorage())
	}
	platformInterface := service.FromContext[adapter.PlatformInterface](ctx)
	var defaultLogWriter io.Writer
	if platformInterface != nil {
		defaultLogWriter = io.Discard
	}
	logFactory, err := log.New(log.Options{
		Context:        ctx,
		Options:        common.PtrValueOrDefault(options.Log),
		Observable:     needClashAPI || needAPIService,
		DefaultWriter:  defaultLogWriter,
		BaseTime:       createdAt,
		PlatformWriter: options.PlatformLogWriter,
	})
	if err != nil {
		return nil, E.Cause(err, "create log factory")
	}
	instance := &Box{
		createdAt:           createdAt,
		debugOptions:        debugOptions,
		logFactory:          logFactory,
		logger:              logFactory.Logger(),
		done:                make(chan struct{}),
		beforeRuntimeCommit: options.BeforeRuntimeCommit,
	}
	constructionComplete := false
	defer func() {
		if constructionComplete {
			return
		}
		cleanupErr, cleanupPanic := instance.closeResources()
		if cleanupPanic != nil {
			cleanupErr = errors.Join(cleanupErr, E.New("panic while rolling back Box construction: ", cleanupPanic))
		}
		result = nil
		resultErr = errors.Join(resultErr, cleanupErr)
	}()
	service.MustRegister[log.Factory](ctx, logFactory)

	routeOptions := common.PtrValueOrDefault(options.Route)
	certificateOptions := common.PtrValueOrDefault(options.Certificate)
	if C.IsAndroid || certificateOptions.Store != "" && certificateOptions.Store != C.CertificateStoreSystem ||
		len(certificateOptions.Certificate) > 0 ||
		len(certificateOptions.CertificatePath) > 0 ||
		len(certificateOptions.CertificateDirectoryPath) > 0 {
		certificateStore, err := certificate.NewStore(ctx, logFactory.NewLogger("certificate"), certificateOptions)
		if err != nil {
			return nil, err
		}
		service.MustRegister[adapter.CertificateStore](ctx, certificateStore)
		instance.internalService = append(instance.internalService, certificateStore)
	}
	netnsManager, err := netns.NewManager(logFactory.NewLogger("netns"), options.NetworkNamespaces, options.NetworkNamespaceHolderArgs)
	if err != nil {
		return nil, err
	}
	service.MustRegister[adapter.NetworkNamespaceManager](ctx, netnsManager)
	instance.internalService = append(instance.internalService, netnsManager)
	dnsOptions := common.PtrValueOrDefault(options.DNS)
	endpointManager := endpoint.NewManager(logFactory.NewLogger("endpoint"), endpointRegistry)
	inboundManager := inbound.NewManager(logFactory.NewLogger("inbound"), inboundRegistry, endpointManager)
	outboundManager := outbound.NewManager(logFactory.NewLogger("outbound"), outboundRegistry, endpointManager, routeOptions.Final)
	dnsTransportManager := dns.NewTransportManager(logFactory.NewLogger("dns/transport"), dnsTransportRegistry, outboundManager, dnsOptions.Final)
	serviceManager := boxService.NewManager(logFactory.NewLogger("service"), serviceRegistry)
	certificateProviderManager := boxCertificate.NewManager(logFactory.NewLogger("certificate-provider"), certificateProviderRegistry)
	instance.endpoint = endpointManager
	instance.inbound = inboundManager
	instance.outbound = outboundManager
	instance.dnsTransport = dnsTransportManager
	instance.service = serviceManager
	instance.certificateProvider = certificateProviderManager
	service.MustRegister[adapter.EndpointManager](ctx, endpointManager)
	service.MustRegister[adapter.InboundManager](ctx, inboundManager)
	service.MustRegister[adapter.OutboundManager](ctx, outboundManager)
	service.MustRegister[adapter.DNSTransportManager](ctx, dnsTransportManager)
	service.MustRegister[adapter.ServiceManager](ctx, serviceManager)
	service.MustRegister[adapter.CertificateProviderManager](ctx, certificateProviderManager)
	dnsRouter, err := dns.NewRouter(ctx, logFactory, dnsOptions)
	if err != nil {
		return nil, E.Cause(err, "initialize DNS router")
	}
	instance.dnsRouter = dnsRouter
	service.MustRegister[adapter.DNSRouter](ctx, dnsRouter)
	service.MustRegister[adapter.DNSRuleSetUpdateValidator](ctx, dnsRouter)
	networkManager, err := route.NewNetworkManager(ctx, logFactory.NewLogger("network"), routeOptions, dnsOptions)
	if err != nil {
		return nil, E.Cause(err, "initialize network manager")
	}
	instance.network = networkManager
	service.MustRegister[adapter.NetworkManager](ctx, networkManager)
	connectionManager := route.NewConnectionManager(logFactory.NewLogger("connection"))
	instance.connection = connectionManager
	service.MustRegister[adapter.ConnectionManager](ctx, connectionManager)
	// Must register after ConnectionManager: the Apple HTTP engine's proxy bridge reads it from the context when Manager.Start resolves the default client.
	httpClientManager := httpclient.NewManager(ctx, logFactory.NewLogger("httpclient"), options.HTTPClients, routeOptions.DefaultHTTPClient)
	service.MustRegister[adapter.HTTPClientManager](ctx, httpClientManager)
	httpClientService := adapter.LifecycleService(httpClientManager)
	instance.httpClientService = httpClientService
	router := route.NewRouter(ctx, logFactory, routeOptions, dnsOptions)
	instance.router = router
	service.MustRegister[adapter.Router](ctx, router)
	err = router.Initialize(routeOptions.Rules, routeOptions.RuleSet)
	if err != nil {
		return nil, E.Cause(err, "initialize router")
	}
	if needClashAPI || needAPIService {
		trafficManager := trafficcontrol.NewManager(outboundManager)
		service.MustRegisterPtr(ctx, trafficManager)
		router.AppendTracker(trafficManager)
		instance.internalService = append(instance.internalService, trafficManager)
	}
	ntpOptions := common.PtrValueOrDefault(options.NTP)
	var timeService *tls.TimeServiceWrapper
	if ntpOptions.Enabled {
		timeService = new(tls.TimeServiceWrapper)
		service.MustRegister[ntp.TimeService](ctx, timeService)
	}
	for i, transportOptions := range dnsOptions.Servers {
		var tag string
		if transportOptions.Tag != "" {
			tag = transportOptions.Tag
		} else {
			tag = F.ToString(i)
		}
		err = dnsTransportManager.Create(
			ctx,
			logFactory.NewLogger(F.ToString("dns/", transportOptions.Type, "[", tag, "]")),
			tag,
			transportOptions.Type,
			transportOptions.Options,
		)
		if err != nil {
			return nil, E.Cause(err, "initialize DNS server[", i, "]")
		}
	}
	err = dnsRouter.Initialize(dnsOptions.Rules)
	if err != nil {
		return nil, E.Cause(err, "initialize dns router")
	}
	for i, endpointOptions := range options.Endpoints {
		var tag string
		if endpointOptions.Tag != "" {
			tag = endpointOptions.Tag
		} else {
			tag = F.ToString(i)
		}
		endpointCtx := ctx
		if tag != "" {
			// TODO: remove this
			endpointCtx = adapter.WithContext(endpointCtx, &adapter.InboundContext{
				Outbound: tag,
			})
		}
		err = endpointManager.Create(
			endpointCtx,
			router,
			logFactory.NewLogger(F.ToString("endpoint/", endpointOptions.Type, "[", tag, "]")),
			tag,
			endpointOptions.Type,
			endpointOptions.Options,
		)
		if err != nil {
			return nil, E.Cause(err, "initialize endpoint[", i, "]")
		}
	}
	for i, inboundOptions := range options.Inbounds {
		var tag string
		if inboundOptions.Tag != "" {
			tag = inboundOptions.Tag
		} else {
			tag = F.ToString(i)
		}
		err = inboundManager.Create(
			ctx,
			router,
			logFactory.NewLogger(F.ToString("inbound/", inboundOptions.Type, "[", tag, "]")),
			tag,
			inboundOptions.Type,
			inboundOptions.Options,
		)
		if err != nil {
			return nil, E.Cause(err, "initialize inbound[", i, "]")
		}
	}
	for i, serviceOptions := range options.Services {
		var tag string
		if serviceOptions.Tag != "" {
			tag = serviceOptions.Tag
		} else {
			tag = F.ToString(i)
		}
		err = serviceManager.Create(
			ctx,
			logFactory.NewLogger(F.ToString("service/", serviceOptions.Type, "[", tag, "]")),
			tag,
			serviceOptions.Type,
			serviceOptions.Options,
		)
		if err != nil {
			return nil, E.Cause(err, "initialize service[", i, "]")
		}
	}
	for i, outboundOptions := range options.Outbounds {
		var tag string
		if outboundOptions.Tag != "" {
			tag = outboundOptions.Tag
		} else {
			tag = F.ToString(i)
		}
		outboundCtx := ctx
		if tag != "" {
			// TODO: remove this
			outboundCtx = adapter.WithContext(outboundCtx, &adapter.InboundContext{
				Outbound: tag,
			})
		}
		err = outboundManager.Create(
			outboundCtx,
			router,
			logFactory.NewLogger(F.ToString("outbound/", outboundOptions.Type, "[", tag, "]")),
			tag,
			outboundOptions.Type,
			outboundOptions.Options,
		)
		if err != nil {
			return nil, E.Cause(err, "initialize outbound[", i, "]")
		}
	}
	for i, certificateProviderOptions := range options.CertificateProviders {
		var tag string
		if certificateProviderOptions.Tag != "" {
			tag = certificateProviderOptions.Tag
		} else {
			tag = F.ToString(i)
		}
		err = certificateProviderManager.Create(
			ctx,
			logFactory.NewLogger(F.ToString("certificate-provider/", certificateProviderOptions.Type, "[", tag, "]")),
			tag,
			certificateProviderOptions.Type,
			certificateProviderOptions.Options,
		)
		if err != nil {
			return nil, E.Cause(err, "initialize certificate provider[", i, "]")
		}
	}
	outboundManager.Initialize(func() (adapter.Outbound, error) {
		return direct.NewOutbound(
			ctx,
			router,
			logFactory.NewLogger("outbound/direct"),
			"direct",
			option.DirectOutboundOptions{},
		)
	})
	dnsTransportManager.Initialize(func() (adapter.DNSTransport, error) {
		return dnsTransportRegistry.CreateDNSTransport(
			ctx,
			logFactory.NewLogger("dns/local"),
			"local",
			C.DNSTypeLocal,
			&option.LocalDNSServerOptions{},
		)
	})
	httpClientManager.Initialize(func() (*httpclient.ManagedTransport, error) {
		deprecated.Report(ctx, deprecated.OptionImplicitDefaultHTTPClient)
		var httpClientOptions option.HTTPClientOptions
		httpClientOptions.DefaultOutbound = true
		return httpclient.NewTransport(ctx, logFactory.NewLogger("httpclient"), "", httpClientOptions)
	})
	if platformInterface != nil {
		err = platformInterface.Initialize(networkManager)
		if err != nil {
			return nil, E.Cause(err, "initialize platform interface")
		}
	}
	if needCacheFile {
		cacheFile := cachefile.New(ctx, logFactory.NewLogger("cache-file"), common.PtrValueOrDefault(experimentalOptions.CacheFile))
		service.MustRegister[adapter.CacheFile](ctx, cacheFile)
		instance.internalService = append(instance.internalService, cacheFile)
	}
	if needClashAPI {
		clashAPIOptions := common.PtrValueOrDefault(experimentalOptions.ClashAPI)
		clashAPIOptions.ModeList = experimental.CalculateClashModeList(options.Options)
		clashServer, err := experimental.NewClashServer(ctx, logFactory.(log.ObservableFactory), clashAPIOptions)
		if err != nil {
			return nil, E.Cause(err, "create clash-server")
		}
		service.MustRegister[adapter.ClashServer](ctx, clashServer)
		instance.internalService = append(instance.internalService, clashServer)
	}
	if needV2RayAPI {
		v2rayServer, err := experimental.NewV2RayServer(logFactory.NewLogger("v2ray-api"), common.PtrValueOrDefault(experimentalOptions.V2RayAPI))
		if err != nil {
			return nil, E.Cause(err, "create v2ray-server")
		}
		if v2rayServer.StatsService() != nil {
			router.AppendTracker(v2rayServer.StatsService())
		}
		instance.internalService = append(instance.internalService, v2rayServer)
		service.MustRegister[adapter.V2RayServer](ctx, v2rayServer)
	}
	if ntpOptions.Enabled {
		if ntpOptions.WriteToSystem {
			err = adapter.CheckSecurityFeature(ctx, "NTP `write_to_system`")
			if err != nil {
				return nil, err
			}
		}
		ntpDialer, err := dialer.New(ctx, ntpOptions.DialerOptions, ntpOptions.ServerIsDomain())
		if err != nil {
			return nil, E.Cause(err, "create NTP service")
		}
		ntpService := ntp.NewService(ntp.Options{
			Context:       ctx,
			Dialer:        ntpDialer,
			Logger:        logFactory.NewLogger("ntp"),
			Server:        ntpOptions.ServerOptions.Build(),
			Interval:      time.Duration(ntpOptions.Interval),
			WriteToSystem: ntpOptions.WriteToSystem,
		})
		timeService.TimeService = ntpService
		instance.internalService = append(instance.internalService, adapter.NewLifecycleService(ntpService, "ntp service"))
	}
	constructionComplete = true
	return instance, nil
}

func cloneDashboardOptions(options option.Options) option.Options {
	cloned := options
	cloned.Inbounds = append([]option.Inbound(nil), options.Inbounds...)
	cloned.Endpoints = append([]option.Endpoint(nil), options.Endpoints...)
	cloned.Services = append([]option.Service(nil), options.Services...)
	for index := range cloned.Services {
		apiOptions, loaded := cloned.Services[index].Options.(*option.APIServiceOptions)
		if !loaded {
			continue
		}
		apiCopy := *apiOptions
		if apiOptions.Dashboard != nil {
			dashboardCopy := *apiOptions.Dashboard
			if apiOptions.Dashboard.AppliedServerRevisions != nil {
				dashboardCopy.AppliedServerRevisions = make(map[string]int64, len(apiOptions.Dashboard.AppliedServerRevisions))
				maps.Copy(dashboardCopy.AppliedServerRevisions, apiOptions.Dashboard.AppliedServerRevisions)
			}
			apiCopy.Dashboard = &dashboardCopy
		}
		cloned.Services[index].Options = &apiCopy
	}
	return cloned
}

func validateDashboardServices(services []option.Service) error {
	var dashboardCount int
	for _, serviceOptions := range services {
		if serviceOptions.Type != C.TypeAPI {
			continue
		}
		apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
		if loaded && apiOptions.Dashboard != nil && apiOptions.Dashboard.Enabled {
			dashboardCount++
		}
	}
	if dashboardCount > 1 {
		return E.New("only one dashboard-enabled API service is allowed")
	}
	return nil
}

func (s *Box) PreStart() (result error) {
	err := s.preStart()
	if err != nil {
		defer func() {
			v := recover()
			if v != nil {
				result = errors.Join(err, E.New("panic on early close: ", v))
			}
		}()
		return errors.Join(err, s.Close())
	}
	s.logger.Info("Sidera Core pre-started (", F.Seconds(time.Since(s.createdAt).Seconds()), "s)")
	return nil
}

func (s *Box) Start() (result error) {
	err := s.start()
	if err == nil {
		err = s.service.CommitRuntime(s.beforeRuntimeCommit)
	}
	if err != nil {
		defer func() {
			v := recover()
			if v != nil {
				result = errors.Join(err, E.New("panic on early start cleanup: ", v))
			}
		}()
		return errors.Join(err, s.Close())
	}
	s.logger.Info("Sidera Core started (", F.Seconds(time.Since(s.createdAt).Seconds()), "s)")
	return nil
}

func (s *Box) preStart() error {
	monitor := taskmonitor.New(s.logger, C.StartTimeout)
	monitor.Start("start logger")
	err := s.logFactory.Start()
	monitor.Finish()
	if err != nil {
		return E.Cause(err, "start logger")
	}
	applyDebugOptions(s.debugOptions)
	s.debugHTTPServer, err = startDebugHTTPServer(s.debugOptions)
	if err != nil {
		return err
	}
	err = adapter.StartNamed(s.logger, adapter.StartStateInitialize, s.internalService) // cache-file clash-api v2ray-api
	if err != nil {
		return err
	}
	err = adapter.Start(s.logger, adapter.StartStateInitialize, s.network, s.dnsTransport, s.dnsRouter, s.connection, s.router, s.outbound, s.inbound, s.endpoint, s.service, s.certificateProvider)
	if err != nil {
		return err
	}
	err = adapter.Start(s.logger, adapter.StartStateStart, s.outbound, s.dnsTransport, s.network, s.connection)
	if err != nil {
		return err
	}
	err = adapter.StartNamed(s.logger, adapter.StartStateStart, []adapter.LifecycleService{s.httpClientService})
	if err != nil {
		return err
	}
	err = adapter.Start(s.logger, adapter.StartStateStart, s.router, s.dnsRouter)
	if err != nil {
		return err
	}
	return nil
}

func (s *Box) start() error {
	err := s.preStart()
	if err != nil {
		return err
	}
	err = adapter.StartNamed(s.logger, adapter.StartStateStart, s.internalService)
	if err != nil {
		return err
	}
	err = adapter.Start(s.logger, adapter.StartStateStart, s.endpoint)
	if err != nil {
		return err
	}
	err = adapter.Start(s.logger, adapter.StartStateStart, s.certificateProvider)
	if err != nil {
		return err
	}
	err = adapter.Start(s.logger, adapter.StartStateStart, s.inbound, s.service)
	if err != nil {
		return err
	}
	err = adapter.Start(s.logger, adapter.StartStatePostStart, s.outbound, s.network, s.dnsTransport, s.dnsRouter, s.connection, s.router, s.endpoint, s.certificateProvider, s.inbound, s.service)
	if err != nil {
		return err
	}
	err = adapter.StartNamed(s.logger, adapter.StartStatePostStart, s.internalService)
	if err != nil {
		return err
	}
	err = adapter.Start(s.logger, adapter.StartStateStarted, s.network, s.dnsTransport, s.dnsRouter, s.connection, s.router, s.outbound, s.endpoint, s.certificateProvider, s.inbound, s.service)
	if err != nil {
		return err
	}
	err = adapter.StartNamed(s.logger, adapter.StartStateStarted, s.internalService)
	if err != nil {
		return err
	}
	return nil
}

func (s *Box) Close() (err error) {
	s.closeAccess.Lock()
	if s.closed {
		closeErr := s.closeErr
		s.closeAccess.Unlock()
		return errors.Join(os.ErrClosed, closeErr)
	}
	if s.closing {
		s.closeAccess.Unlock()
		// A child can re-enter Close from another goroutine and wait for that
		// call. Waiting here would make the owning teardown wait on itself.
		return os.ErrClosed
	}
	s.closing = true
	s.closeAccess.Unlock()

	defer func() {
		panicValue := recover()
		s.closeAccess.Lock()
		s.closeErr = err
		s.closing = false
		s.closed = true
		s.closeAccess.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	var closePanic any
	err, closePanic = s.closeResources()
	if closePanic != nil {
		panic(closePanic)
	}
	return err
}

func (s *Box) closeResources() (err error, firstPanic any) {
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
	closeResource := func(name string, closeFunc func() error) {
		done := adapter.LogElapsed(s.logger, "close ", name)
		childErr, panicValue := closeBoxResource(closeFunc)
		done()
		if childErr != nil {
			err = errors.Join(err, E.Cause(childErr, "close ", name))
		}
		if panicValue != nil {
			if firstPanic == nil {
				firstPanic = panicValue
			} else {
				err = errors.Join(err, E.New("additional panic while closing ", name, ": ", panicValue))
			}
		}
	}
	if s.debugHTTPServer != nil {
		closeResource("debug HTTP server", s.debugHTTPServer.Close)
		s.debugHTTPServer = nil
	}
	if s.service != nil {
		closeResource("service", s.service.Close)
	}
	if s.inbound != nil {
		closeResource("inbound", s.inbound.Close)
	}
	if s.certificateProvider != nil {
		closeResource("certificate-provider", s.certificateProvider.Close)
	}
	if s.endpoint != nil {
		closeResource("endpoint", s.endpoint.Close)
	}
	if s.outbound != nil {
		closeResource("outbound", s.outbound.Close)
	}
	if s.router != nil {
		closeResource("router", s.router.Close)
	}
	if s.connection != nil {
		closeResource("connection", s.connection.Close)
	}
	if s.dnsRouter != nil {
		closeResource("dns-router", s.dnsRouter.Close)
	}
	if s.dnsTransport != nil {
		closeResource("dns-transport", s.dnsTransport.Close)
	}
	if s.network != nil {
		closeResource("network", s.network.Close)
	}
	if s.httpClientService != nil {
		closeResource("http-client", s.httpClientService.Close)
	}
	for index, lifecycleService := range s.internalService {
		if lifecycleService != nil {
			closeResource(F.ToString("internal service[", index, "]"), lifecycleService.Close)
		}
	}
	if s.logFactory != nil {
		closeResource("logger", s.logFactory.Close)
	}
	return err, firstPanic
}

func closeBoxResource(closeFunc func() error) (err error, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	err = closeFunc()
	return err, nil
}

func (s *Box) Network() adapter.NetworkManager {
	return s.network
}

func (s *Box) Router() adapter.Router {
	return s.router
}

func (s *Box) Inbound() adapter.InboundManager {
	return s.inbound
}

func (s *Box) Outbound() adapter.OutboundManager {
	return s.outbound
}

func (s *Box) Endpoint() adapter.EndpointManager {
	return s.endpoint
}

func (s *Box) LogFactory() log.Factory {
	return s.logFactory
}
