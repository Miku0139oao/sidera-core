package ocm

import (
	"bytes"
	"context"
	stdTLS "crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	boxService "github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/common/dialer"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/Miku0139oao/sidera-core/common/tls"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	aTLS "github.com/sagernet/sing/common/tls"

	"github.com/go-chi/chi/v5"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/net/http2"
	"golang.org/x/sync/singleflight"
)

const (
	maxTrackedRequestBodyBytes = 32 << 20
	maxTrackedResponseBytes    = 8 << 20
	maxTrackedSSELineBytes     = 1 << 20
	tokenRefreshTimeout        = 30 * time.Second
	maxConcurrentWebSockets    = 128
)

type boundedCapture struct {
	data     []byte
	limit    int
	overflow bool
}

func (c *boundedCapture) Write(data []byte) (int, error) {
	if !c.overflow {
		remaining := c.limit - len(c.data)
		if len(data) > remaining {
			c.data = append(c.data, data[:max(remaining, 0)]...)
			c.overflow = true
		} else {
			c.data = append(c.data, data...)
		}
	}
	return len(data), nil
}

type boundedSSEParser struct {
	line       []byte
	discarding bool
	overflow   bool
}

func (p *boundedSSEParser) consume(data []byte, handleLine func([]byte)) {
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		var part []byte
		if newline < 0 {
			part = data
			data = nil
		} else {
			part = data[:newline]
			data = data[newline+1:]
		}
		if !p.discarding {
			remaining := maxTrackedSSELineBytes - len(p.line)
			if len(part) > remaining {
				p.line = nil
				p.discarding = true
				p.overflow = true
			} else {
				p.line = append(p.line, part...)
			}
		}
		if newline >= 0 {
			if !p.discarding {
				handleLine(p.line)
			}
			p.line = p.line[:0]
			p.discarding = false
		}
	}
}

func (p *boundedSSEParser) finish(handleLine func([]byte)) {
	if !p.discarding && len(p.line) > 0 {
		handleLine(p.line)
	}
	p.line = nil
	p.discarding = false
}

func readBoundedBody(body io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, true, nil
	}
	return data, false, nil
}

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.OCMServiceOptions](registry, C.TypeOCM, NewService)
}

type errorResponse struct {
	Error errorDetails `json:"error"`
}

type errorDetails struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

func writeJSONError(w http.ResponseWriter, r *http.Request, statusCode int, errorType string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	json.NewEncoder(w).Encode(errorResponse{
		Error: errorDetails{
			Type:    errorType,
			Message: message,
		},
	})
}

func isHopByHopHeader(header string) bool {
	switch strings.ToLower(header) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailers", "transfer-encoding", "upgrade", "host":
		return true
	default:
		return false
	}
}

func normalizeRateLimitIdentifier(limitIdentifier string) string {
	trimmedIdentifier := strings.TrimSpace(strings.ToLower(limitIdentifier))
	if trimmedIdentifier == "" {
		return ""
	}
	return strings.ReplaceAll(trimmedIdentifier, "_", "-")
}

func parseInt64Header(headers http.Header, headerName string) (int64, bool) {
	headerValue := strings.TrimSpace(headers.Get(headerName))
	if headerValue == "" {
		return 0, false
	}
	parsedValue, parseError := strconv.ParseInt(headerValue, 10, 64)
	if parseError != nil {
		return 0, false
	}
	return parsedValue, true
}

func weeklyCycleHintForLimit(headers http.Header, limitIdentifier string) *WeeklyCycleHint {
	normalizedLimitIdentifier := normalizeRateLimitIdentifier(limitIdentifier)
	if normalizedLimitIdentifier == "" {
		return nil
	}

	windowHeader := "x-" + normalizedLimitIdentifier + "-secondary-window-minutes"
	resetHeader := "x-" + normalizedLimitIdentifier + "-secondary-reset-at"

	windowMinutes, hasWindowMinutes := parseInt64Header(headers, windowHeader)
	resetAtUnix, hasResetAt := parseInt64Header(headers, resetHeader)
	if !hasWindowMinutes || !hasResetAt || windowMinutes <= 0 || resetAtUnix <= 0 {
		return nil
	}

	return &WeeklyCycleHint{
		WindowMinutes: windowMinutes,
		ResetAt:       time.Unix(resetAtUnix, 0).UTC(),
	}
}

func extractWeeklyCycleHint(headers http.Header) *WeeklyCycleHint {
	activeLimitIdentifier := normalizeRateLimitIdentifier(headers.Get("x-codex-active-limit"))
	if activeLimitIdentifier != "" {
		if activeHint := weeklyCycleHintForLimit(headers, activeLimitIdentifier); activeHint != nil {
			return activeHint
		}
	}
	return weeklyCycleHintForLimit(headers, "codex")
}

type Service struct {
	boxService.Adapter
	ctx              context.Context
	logger           log.ContextLogger
	credentialPath   string
	credentials      *oauthCredentials
	users            []option.OCMUser
	dialer           N.Dialer
	httpClient       *http.Client
	httpHeaders      http.Header
	listener         *listener.Listener
	tlsConfig        tls.ServerConfig
	httpServer       *http.Server
	userManager      *UserManager
	accessMutex      sync.RWMutex
	usageTracker     *AggregatedUsage
	refreshCtx       context.Context
	refreshCancel    context.CancelFunc
	refreshGroup     singleflight.Group
	refreshLifecycle sync.Mutex
	refreshClosing   bool
	refreshWG        sync.WaitGroup
	webSocketSlots   chan struct{}
	webSocketMutex   sync.Mutex
	webSocketGroup   sync.WaitGroup
	webSocketConns   map[*webSocketSession]struct{}
	shuttingDown     bool
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.OCMServiceOptions) (adapter.Service, error) {
	serviceDialer, err := dialer.NewWithOptions(dialer.Options{
		Context: ctx,
		Options: option.DialerOptions{
			Detour: options.Detour,
		},
		RemoteIsDomain: true,
	})
	if err != nil {
		return nil, E.Cause(err, "create dialer")
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig: &stdTLS.Config{
				RootCAs: adapter.RootPoolFromContext(ctx),
				Time:    ntp.TimeFuncFromContext(ctx),
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return serviceDialer.DialContext(ctx, network, M.ParseSocksaddr(addr))
			},
		},
	}

	userManager := &UserManager{
		tokenMap: make(map[string]string),
	}

	var usageTracker *AggregatedUsage
	if options.UsagesPath != "" {
		usageTracker = &AggregatedUsage{
			LastUpdated:  time.Now(),
			Combinations: make([]CostCombination, 0),
			ctx:          ctx,
			filePath:     options.UsagesPath,
			logger:       logger,
		}
	}

	refreshCtx, refreshCancel := context.WithCancel(ctx)
	service := &Service{
		Adapter:        boxService.NewAdapter(C.TypeOCM, tag),
		ctx:            ctx,
		logger:         logger,
		credentialPath: options.CredentialPath,
		users:          options.Users,
		dialer:         serviceDialer,
		httpClient:     httpClient,
		httpHeaders:    options.Headers.Build(),
		listener: listener.New(listener.Options{
			Context: ctx,
			Logger:  logger,
			Network: []string{N.NetworkTCP},
			Listen:  options.ListenOptions,
		}),
		userManager:    userManager,
		usageTracker:   usageTracker,
		refreshCtx:     refreshCtx,
		refreshCancel:  refreshCancel,
		webSocketSlots: make(chan struct{}, maxConcurrentWebSockets),
		webSocketConns: make(map[*webSocketSession]struct{}),
	}

	if options.TLS != nil {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
		service.tlsConfig = tlsConfig
	}

	return service, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}

	s.userManager.UpdateUsers(s.users)

	credentials, err := platformReadCredentials(s.ctx, s.credentialPath)
	if err != nil {
		return E.Cause(err, "read credentials")
	}
	s.credentials = credentials

	if s.usageTracker != nil {
		err = s.usageTracker.Load()
		if err != nil {
			s.logger.Error("load usage statistics: ", err, ", usage tracking and saving disabled")
			s.usageTracker = nil
		}
	}

	router := chi.NewRouter()
	router.Mount("/", s)

	s.httpServer = &http.Server{Handler: router}

	if s.tlsConfig != nil {
		err = s.tlsConfig.Start()
		if err != nil {
			return E.Cause(err, "create TLS config")
		}
	}

	tcpListener, err := s.listener.ListenTCP()
	if err != nil {
		return err
	}

	if s.usageTracker != nil {
		s.usageTracker.startSaveWorker()
	}

	if s.tlsConfig != nil {
		if !common.Contains(s.tlsConfig.NextProtos(), http2.NextProtoTLS) {
			s.tlsConfig.SetNextProtos(append([]string{"h2"}, s.tlsConfig.NextProtos()...))
		}
		tcpListener = aTLS.NewListener(tcpListener, s.tlsConfig)
	}

	go func() {
		serveErr := s.httpServer.Serve(tcpListener)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.logger.Error("serve error: ", serveErr)
		}
	}()

	return nil
}

func (s *Service) getAccessToken(ctx context.Context) (string, error) {
	s.accessMutex.RLock()
	if s.credentials == nil {
		s.accessMutex.RUnlock()
		return "", net.ErrClosed
	}
	if !s.credentials.needsRefresh() {
		token := s.credentials.getAccessToken()
		s.accessMutex.RUnlock()
		return token, nil
	}
	s.accessMutex.RUnlock()

	result := s.refreshGroup.DoChan("token", s.refreshAccessToken)
	select {
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case <-s.refreshCtx.Done():
		return "", context.Cause(s.refreshCtx)
	case refreshed := <-result:
		if refreshed.Err != nil {
			return "", refreshed.Err
		}
		return refreshed.Val.(string), nil
	}
}

func (s *Service) refreshAccessToken() (any, error) {
	s.refreshLifecycle.Lock()
	if s.refreshClosing {
		s.refreshLifecycle.Unlock()
		return "", net.ErrClosed
	}
	s.refreshWG.Add(1)
	s.refreshLifecycle.Unlock()
	defer s.refreshWG.Done()

	s.accessMutex.RLock()
	if s.credentials == nil {
		s.accessMutex.RUnlock()
		return "", net.ErrClosed
	}
	if !s.credentials.needsRefresh() {
		token := s.credentials.getAccessToken()
		s.accessMutex.RUnlock()
		return token, nil
	}
	credentials := *s.credentials
	if s.credentials.Tokens != nil {
		tokens := *s.credentials.Tokens
		credentials.Tokens = &tokens
	}
	if s.credentials.LastRefresh != nil {
		lastRefresh := *s.credentials.LastRefresh
		credentials.LastRefresh = &lastRefresh
	}
	s.accessMutex.RUnlock()

	refreshCtx, cancel := context.WithTimeout(s.refreshCtx, tokenRefreshTimeout)
	defer cancel()
	newCredentials, err := refreshToken(refreshCtx, s.httpClient, &credentials)
	if err != nil {
		return "", err
	}
	if err = context.Cause(s.refreshCtx); err != nil {
		return "", err
	}
	s.accessMutex.Lock()
	s.credentials = newCredentials
	s.accessMutex.Unlock()
	if err = platformWriteCredentials(s.refreshCtx, newCredentials, s.credentialPath); err != nil {
		s.logger.Warn("persist refreshed token: ", err)
	}
	return newCredentials.getAccessToken(), nil
}

func (s *Service) getAccountID() string {
	s.accessMutex.RLock()
	defer s.accessMutex.RUnlock()
	return s.credentials.getAccountID()
}

func (s *Service) isAPIKeyMode() bool {
	s.accessMutex.RLock()
	defer s.accessMutex.RUnlock()
	return s.credentials.isAPIKeyMode()
}

func (s *Service) getBaseURL() string {
	if s.isAPIKeyMode() {
		return openaiAPIBaseURL
	}
	return chatGPTBackendURL
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/v1/") {
		writeJSONError(w, r, http.StatusNotFound, "invalid_request_error", "path must start with /v1/")
		return
	}

	var proxyPath string
	if s.isAPIKeyMode() {
		proxyPath = path
	} else {
		if path == "/v1/chat/completions" {
			writeJSONError(w, r, http.StatusBadRequest, "invalid_request_error",
				"chat completions endpoint is only available in API key mode")
			return
		}
		proxyPath = strings.TrimPrefix(path, "/v1")
	}

	var username string
	if len(s.users) > 0 {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			s.logger.Warn("authentication failed for request from ", r.RemoteAddr, ": missing Authorization header")
			writeJSONError(w, r, http.StatusUnauthorized, "authentication_error", "missing api key")
			return
		}
		clientToken := strings.TrimPrefix(authHeader, "Bearer ")
		if clientToken == authHeader {
			s.logger.Warn("authentication failed for request from ", r.RemoteAddr, ": invalid Authorization format")
			writeJSONError(w, r, http.StatusUnauthorized, "authentication_error", "invalid api key format")
			return
		}
		var ok bool
		username, ok = s.userManager.Authenticate(clientToken)
		if !ok {
			s.logger.Warn("authentication failed for request from ", r.RemoteAddr, ": unknown key: ", clientToken)
			writeJSONError(w, r, http.StatusUnauthorized, "authentication_error", "invalid api key")
			return
		}
	}

	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && strings.HasPrefix(path, "/v1/responses") {
		s.handleWebSocket(w, r, proxyPath, username)
		return
	}

	var requestModel string

	if s.usageTracker != nil && r.Body != nil {
		if r.ContentLength > maxTrackedRequestBodyBytes {
			_ = r.Body.Close()
			s.logger.Warn("reject oversized OCM request body from ", r.RemoteAddr)
			writeJSONError(w, r, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body exceeds usage-tracking limit")
			return
		}
		originalBody := r.Body
		bodyBytes, oversized, err := readBoundedBody(originalBody, maxTrackedRequestBodyBytes)
		_ = originalBody.Close()
		if err != nil {
			writeJSONError(w, r, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
			return
		}
		if oversized {
			s.logger.Warn("reject oversized OCM request body from ", r.RemoteAddr)
			writeJSONError(w, r, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body exceeds usage-tracking limit")
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(bodyBytes, &request) == nil {
			requestModel = request.Model
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	accessToken, err := s.getAccessToken(r.Context())
	if err != nil {
		s.logger.Error("get access token: ", err)
		writeJSONError(w, r, http.StatusUnauthorized, "authentication_error", "Authentication failed")
		return
	}

	proxyURL := s.getBaseURL() + proxyPath
	if r.URL.RawQuery != "" {
		proxyURL += "?" + r.URL.RawQuery
	}
	proxyRequest, err := http.NewRequestWithContext(r.Context(), r.Method, proxyURL, r.Body)
	if err != nil {
		s.logger.Error("create proxy request: ", err)
		writeJSONError(w, r, http.StatusInternalServerError, "api_error", "Internal server error")
		return
	}

	for key, values := range r.Header {
		if !isHopByHopHeader(key) && key != "Authorization" {
			proxyRequest.Header[key] = values
		}
	}

	for key, values := range s.httpHeaders {
		proxyRequest.Header.Del(key)
		proxyRequest.Header[key] = values
	}

	proxyRequest.Header.Set("Authorization", "Bearer "+accessToken)

	if accountID := s.getAccountID(); accountID != "" {
		proxyRequest.Header.Set("ChatGPT-Account-Id", accountID)
	}

	response, err := s.httpClient.Do(proxyRequest)
	if err != nil {
		writeJSONError(w, r, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	defer response.Body.Close()

	for key, values := range response.Header {
		if !isHopByHopHeader(key) {
			w.Header()[key] = values
		}
	}
	w.WriteHeader(response.StatusCode)

	trackUsage := s.usageTracker != nil && response.StatusCode == http.StatusOK &&
		(path == "/v1/chat/completions" || strings.HasPrefix(path, "/v1/responses"))
	if trackUsage {
		s.handleResponseWithTracking(w, response, path, requestModel, username)
	} else {
		mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err == nil && mediaType != "text/event-stream" {
			_, _ = io.Copy(w, response.Body)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			s.logger.Error("streaming not supported")
			return
		}
		buffer := make([]byte, buf.BufferSize)
		for {
			n, err := response.Body.Read(buffer)
			if n > 0 {
				_, writeError := w.Write(buffer[:n])
				if writeError != nil {
					s.logger.Error("write streaming response: ", writeError)
					return
				}
				flusher.Flush()
			}
			if err != nil {
				return
			}
		}
	}
}

func (s *Service) handleResponseWithTracking(writer http.ResponseWriter, response *http.Response, path string, requestModel string, username string) {
	isChatCompletions := path == "/v1/chat/completions"
	weeklyCycleHint := extractWeeklyCycleHint(response.Header)
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	isStreaming := err == nil && mediaType == "text/event-stream"
	if !isStreaming && !isChatCompletions && response.Header.Get("Content-Type") == "" {
		isStreaming = true
	}
	if !isStreaming {
		capture := &boundedCapture{limit: maxTrackedResponseBytes}
		_, copyErr := io.CopyBuffer(io.MultiWriter(writer, capture), response.Body, make([]byte, buf.BufferSize))
		if copyErr != nil {
			s.logger.Error("stream response body: ", copyErr)
			return
		}
		if capture.overflow {
			s.logger.Warn("OCM response exceeded usage-tracking limit; response was proxied without accounting")
			return
		}

		var responseModel, serviceTier string
		var inputTokens, outputTokens, cachedTokens int64

		if isChatCompletions {
			var chatCompletion openai.ChatCompletion
			if json.Unmarshal(capture.data, &chatCompletion) == nil {
				responseModel = chatCompletion.Model
				serviceTier = string(chatCompletion.ServiceTier)
				inputTokens = chatCompletion.Usage.PromptTokens
				outputTokens = chatCompletion.Usage.CompletionTokens
				cachedTokens = chatCompletion.Usage.PromptTokensDetails.CachedTokens
			}
		} else {
			var responsesResponse responses.Response
			if json.Unmarshal(capture.data, &responsesResponse) == nil {
				responseModel = string(responsesResponse.Model)
				serviceTier = string(responsesResponse.ServiceTier)
				inputTokens = responsesResponse.Usage.InputTokens
				outputTokens = responsesResponse.Usage.OutputTokens
				cachedTokens = responsesResponse.Usage.InputTokensDetails.CachedTokens
			}
		}

		if inputTokens > 0 || outputTokens > 0 {
			if responseModel == "" {
				responseModel = requestModel
			}
			if responseModel != "" {
				contextWindow := detectContextWindow(responseModel, serviceTier, inputTokens)
				if usageErr := s.usageTracker.AddUsageWithCycleHint(
					responseModel,
					contextWindow,
					inputTokens,
					outputTokens,
					cachedTokens,
					serviceTier,
					username,
					time.Now(),
					weeklyCycleHint,
				); usageErr != nil {
					s.logger.Warn("record OCM usage: ", usageErr)
				}
			}
		}
		return
	}

	flusher, ok := writer.(http.Flusher)
	if !ok {
		s.logger.Error("streaming not supported")
		return
	}

	var inputTokens, outputTokens, cachedTokens int64
	var responseModel, serviceTier string
	parser := &boundedSSEParser{}
	handleLine := func(line []byte) {
		line = bytes.TrimSpace(line)
		eventData, isData := bytes.CutPrefix(line, []byte("data: "))
		if !isData || bytes.Equal(eventData, []byte("[DONE]")) {
			return
		}
		if isChatCompletions {
			var chatChunk openai.ChatCompletionChunk
			if json.Unmarshal(eventData, &chatChunk) == nil {
				if chatChunk.Model != "" {
					responseModel = chatChunk.Model
				}
				if chatChunk.ServiceTier != "" {
					serviceTier = string(chatChunk.ServiceTier)
				}
				if chatChunk.Usage.PromptTokens > 0 {
					inputTokens = chatChunk.Usage.PromptTokens
					cachedTokens = chatChunk.Usage.PromptTokensDetails.CachedTokens
				}
				if chatChunk.Usage.CompletionTokens > 0 {
					outputTokens = chatChunk.Usage.CompletionTokens
				}
			}
			return
		}
		var streamEvent responses.ResponseStreamEventUnion
		if json.Unmarshal(eventData, &streamEvent) == nil && streamEvent.Type == "response.completed" {
			completedEvent := streamEvent.AsResponseCompleted()
			if string(completedEvent.Response.Model) != "" {
				responseModel = string(completedEvent.Response.Model)
			}
			if completedEvent.Response.ServiceTier != "" {
				serviceTier = string(completedEvent.Response.ServiceTier)
			}
			if completedEvent.Response.Usage.InputTokens > 0 {
				inputTokens = completedEvent.Response.Usage.InputTokens
				cachedTokens = completedEvent.Response.Usage.InputTokensDetails.CachedTokens
			}
			if completedEvent.Response.Usage.OutputTokens > 0 {
				outputTokens = completedEvent.Response.Usage.OutputTokens
			}
		}
	}
	buffer := make([]byte, buf.BufferSize)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			parser.consume(buffer[:n], handleLine)
			if _, writeErr := writer.Write(buffer[:n]); writeErr != nil {
				s.logger.Error("write streaming response: ", writeErr)
				return
			}
			flusher.Flush()
		}
		if readErr == nil {
			continue
		}
		parser.finish(handleLine)
		if readErr != io.EOF {
			s.logger.Warn("read streaming response: ", readErr)
		}
		if parser.overflow {
			s.logger.Warn("OCM SSE event exceeded usage-tracking limit; stream was proxied without accounting")
			return
		}
		if responseModel == "" {
			responseModel = requestModel
		}
		if (inputTokens > 0 || outputTokens > 0) && responseModel != "" {
			contextWindow := detectContextWindow(responseModel, serviceTier, inputTokens)
			if usageErr := s.usageTracker.AddUsageWithCycleHint(
				responseModel,
				contextWindow,
				inputTokens,
				outputTokens,
				cachedTokens,
				serviceTier,
				username,
				time.Now(),
				weeklyCycleHint,
			); usageErr != nil {
				s.logger.Warn("record OCM usage: ", usageErr)
			}
		}
		return
	}
}

func (s *Service) Close() error {
	s.refreshLifecycle.Lock()
	s.refreshClosing = true
	if s.refreshCancel != nil {
		s.refreshCancel()
	}
	s.refreshLifecycle.Unlock()
	webSocketSessions := s.startWebSocketShutdown()

	err := common.Close(
		common.PtrOrNil(s.httpServer),
		common.PtrOrNil(s.listener),
		s.tlsConfig,
	)
	for _, session := range webSocketSessions {
		session.Close()
	}
	s.webSocketGroup.Wait()
	s.refreshWG.Wait()

	s.httpClient.CloseIdleConnections()
	if s.usageTracker != nil {
		saveErr := s.usageTracker.closeAndSave()
		if saveErr != nil {
			s.logger.Error("save usage statistics: ", saveErr)
		}
	}

	return err
}

func (s *Service) registerWebSocketSession(session *webSocketSession) bool {
	s.webSocketMutex.Lock()
	defer s.webSocketMutex.Unlock()

	if s.shuttingDown {
		return false
	}

	s.webSocketConns[session] = struct{}{}
	s.webSocketGroup.Add(1)
	return true
}

func (s *Service) unregisterWebSocketSession(session *webSocketSession) {
	s.webSocketMutex.Lock()
	_, loaded := s.webSocketConns[session]
	if loaded {
		delete(s.webSocketConns, session)
	}
	s.webSocketMutex.Unlock()

	if loaded {
		s.webSocketGroup.Done()
	}
}

func (s *Service) isShuttingDown() bool {
	s.webSocketMutex.Lock()
	defer s.webSocketMutex.Unlock()
	return s.shuttingDown
}

func (s *Service) startWebSocketShutdown() []*webSocketSession {
	s.webSocketMutex.Lock()
	defer s.webSocketMutex.Unlock()

	s.shuttingDown = true

	webSocketSessions := make([]*webSocketSession, 0, len(s.webSocketConns))
	for session := range s.webSocketConns {
		webSocketSessions = append(webSocketSessions, session)
	}
	return webSocketSessions
}
