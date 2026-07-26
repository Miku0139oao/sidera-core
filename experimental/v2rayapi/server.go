package v2rayapi

import (
	stdjson "encoding/json"
	"errors"
	"expvar"
	"net"
	"net/http"
	"net/http/pprof"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/experimental"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

func init() {
	experimental.RegisterV2RayServerConstructor(NewServer)
}

var _ adapter.V2RayServer = (*Server)(nil)

type Server struct {
	logger          log.Logger
	listen          string
	tcpListener     net.Listener
	grpcServer      *grpc.Server
	statsService    *StatsService
	metricsListen   string
	metricsListener net.Listener
	metricsServer   *http.Server
}

func NewServer(logger log.Logger, options option.V2RayAPIOptions) (adapter.V2RayServer, error) {
	grpcServer := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	statsService := NewStatsService(common.PtrValueOrDefault(options.Stats))
	if statsService != nil {
		RegisterStatsServiceServer(grpcServer, statsService)
		for _, serviceName := range []string{
			"v2ray.core.app.stats.command.StatsService",
			"experimental.v2rayapi.StatsService",
		} {
			serviceDesc := StatsService_ServiceDesc
			serviceDesc.ServiceName = serviceName
			grpcServer.RegisterService(&serviceDesc, statsService)
		}
	}
	var reflectionRegistered bool
	for _, serviceName := range options.XrayServices {
		switch serviceName {
		case "StatsService":
		case "ReflectionService":
			if !reflectionRegistered {
				reflection.Register(grpcServer)
				reflectionRegistered = true
			}
		case "HandlerService", "LoggerService", "RoutingService", "ObservatoryService":
			logger.Warn("Xray ", serviceName, " is not implemented; x-ui operations using it will fall back to a full core restart")
		}
	}
	metricsListen := ""
	if options.Metrics != nil {
		metricsListen = options.Metrics.Listen
		if metricsListen != "" && statsService == nil {
			return nil, errors.New("Xray-compatible metrics requires the stats service")
		}
	}
	server := &Server{
		logger:        logger,
		listen:        options.Listen,
		grpcServer:    grpcServer,
		statsService:  statsService,
		metricsListen: metricsListen,
	}
	return server, nil
}

func (s *Server) Name() string {
	return "v2ray server"
}

func (s *Server) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStatePostStart {
		return nil
	}
	if s.listen != "" {
		listener, err := net.Listen("tcp", s.listen)
		if err != nil {
			return err
		}
		s.logger.Info("grpc server started at ", listener.Addr())
		s.tcpListener = listener
		go func() {
			err := s.grpcServer.Serve(listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				s.logger.Error(err)
			}
		}()
	}
	if s.metricsListen != "" {
		listener, err := net.Listen("tcp", s.metricsListen)
		if err != nil {
			if s.tcpListener != nil {
				s.grpcServer.Stop()
				_ = s.tcpListener.Close()
				s.tcpListener = nil
			}
			return err
		}
		s.metricsListener = listener
		s.metricsServer = &http.Server{Addr: s.metricsListen, Handler: s.metricsHandler()}
		s.logger.Info("Xray-compatible metrics server started at ", listener.Addr())
		go func() {
			err := s.metricsServer.Serve(listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				s.logger.Error(err)
			}
		}()
	}
	return nil
}

func (s *Server) Close() error {
	var err error
	if s.metricsServer != nil {
		err = s.metricsServer.Close()
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}
	if s.grpcServer != nil {
		s.grpcServer.Stop()
	}
	return err
}

func (s *Server) StatsService() adapter.ConnectionTracker {
	return s.statsService
}

func (s *Server) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/vars", s.handleDebugVars)
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func (s *Server) handleDebugVars(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	variables := make(map[string]stdjson.RawMessage)
	expvar.Do(func(value expvar.KeyValue) {
		rawValue := stdjson.RawMessage(value.Value.String())
		if !stdjson.Valid(rawValue) {
			rawValue = stdjson.RawMessage("null")
		}
		variables[value.Key] = rawValue
	})
	stats, err := stdjson.Marshal(s.statsService.Snapshot())
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	variables["stats"] = stats
	variables["observatory"] = stdjson.RawMessage("null")
	if err = stdjson.NewEncoder(writer).Encode(variables); err != nil {
		s.logger.Error(err)
	}
}
