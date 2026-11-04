package ccm

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

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/go-chi/chi/v5"
	"golang.org/x/net/http2"
	"golang.org/x/sync/singleflight"
)

const (
	contextWindowStandard      = 200000
	contextWindowPremium       = 1000000
	premiumContextThreshold    = 200000
	maxTrackedRequestBodyBytes = 32 << 20
	maxTrackedResponseBytes    = 8 << 20
	maxTrackedSSELineBytes     = 1 << 20
	tokenRefreshTimeout        = 30 * time.Second
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

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, nested := token.(json.Delim)
	if !nested || (delimiter != '{' && delimiter != '[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func parseRequestMetadata(data []byte) (string, int, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return "", 0, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return "", 0, E.New("request body must be a JSON object")
	}
	var model string
	var messages int
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			return "", 0, keyErr
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", 0, E.New("invalid JSON object key")
		}
		switch key {
		case "model":
			if err = decoder.Decode(&model); err != nil {
				return "", 0, err
			}
		case "messages":
			arrayToken, arrayErr := decoder.Token()
			if arrayErr != nil {
				return "", 0, arrayErr
			}
			if delimiter, ok := arrayToken.(json.Delim); !ok || delimiter != '[' {
				return "", 0, E.New("messages must be an array")
			}
			for decoder.More() {
				if err = skipJSONValue(decoder); err != nil {
					return "", 0, err
				}
				messages++
			}
			if _, err = decoder.Token(); err != nil {
				return "", 0, err
			}
		default:
			if err = skipJSONValue(decoder); err != nil {
				return "", 0, err
			}
		}
	}
	_, err = decoder.Token()
	return model, messages, err
}

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.CCMServiceOptions](registry, C.TypeCCM, NewService)
}

type errorResponse struct {
	Type      string       `json:"type"`
	Error     errorDetails `json:"error"`
	RequestID string       `json:"request_id,omitempty"`
}

type errorDetails struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func writeJSONError(w http.ResponseWriter, r *http.Request, statusCode int, errorType string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	json.NewEncoder(w).Encode(errorResponse{
		Type: "error",
		Error: errorDetails{
			Type:    errorType,
			Message: message,
		},
		RequestID: r.Header.Get("Request-Id"),
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

const (
	weeklyWindowSeconds = 604800
	weeklyWindowMinutes = weeklyWindowSeconds / 60
)

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

func extractWeeklyCycleHint(headers http.Header) *WeeklyCycleHint {
	resetAtUnix, hasResetAt := parseInt64Header(headers, "anthropic-ratelimit-unified-7d-reset")
	if !hasResetAt || resetAtUnix <= 0 {
		return nil
	}

	return &WeeklyCycleHint{
		WindowMinutes: weeklyWindowMinutes,
		ResetAt:       time.Unix(resetAtUnix, 0).UTC(),
	}
}

type Service struct {
	boxService.Adapter
	ctx              context.Context
	logger           log.ContextLogger
	credentialPath   string
	credentials      *oauthCredentials
	users            []option.CCMUser
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
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.CCMServiceOptions) (adapter.Service, error) {
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
		Adapter:        boxService.NewAdapter(C.TypeCCM, tag),
		ctx:            ctx,
		logger:         logger,
		credentialPath: options.CredentialPath,
		users:          options.Users,
		httpClient:     httpClient,
		httpHeaders:    options.Headers.Build(),
		listener: listener.New(listener.Options{
			Context: ctx,
			Logger:  logger,
			Network: []string{N.NetworkTCP},
			Listen:  options.ListenOptions,
		}),
		userManager:   userManager,
		usageTracker:  usageTracker,
		refreshCtx:    refreshCtx,
		refreshCancel: refreshCancel,
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
		token := s.credentials.AccessToken
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
		token := s.credentials.AccessToken
		s.accessMutex.RUnlock()
		return token, nil
	}
	credentials := *s.credentials
	credentials.Scopes = append([]string(nil), s.credentials.Scopes...)
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
	return newCredentials.AccessToken, nil
}

func detectContextWindow(betaHeader string, totalInputTokens int64) int {
	if totalInputTokens > premiumContextThreshold {
		features := strings.SplitSeq(betaHeader, ",")
		for feature := range features {
			if strings.HasPrefix(strings.TrimSpace(feature), "context-1m") {
				return contextWindowPremium
			}
		}
	}
	return contextWindowStandard
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		writeJSONError(w, r, http.StatusNotFound, "not_found_error", "Not found")
		return
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

	var requestModel string
	var messagesCount int

	if s.usageTracker != nil && r.Body != nil {
		if r.ContentLength > maxTrackedRequestBodyBytes {
			_ = r.Body.Close()
			s.logger.Warn("reject oversized CCM request body from ", r.RemoteAddr)
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
			s.logger.Warn("reject oversized CCM request body from ", r.RemoteAddr)
			writeJSONError(w, r, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body exceeds usage-tracking limit")
			return
		}
		if parsedModel, parsedMessages, parseErr := parseRequestMetadata(bodyBytes); parseErr == nil {
			requestModel = parsedModel
			messagesCount = parsedMessages
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	accessToken, err := s.getAccessToken(r.Context())
	if err != nil {
		s.logger.Error("get access token: ", err)
		writeJSONError(w, r, http.StatusUnauthorized, "authentication_error", "Authentication failed")
		return
	}

	proxyURL := claudeAPIBaseURL + r.URL.RequestURI()
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

	serviceOverridesAcceptEncoding := len(s.httpHeaders.Values("Accept-Encoding")) > 0
	if s.usageTracker != nil && !serviceOverridesAcceptEncoding {
		// Strip Accept-Encoding so Go Transport adds it automatically
		// and transparently decompresses the response for correct usage counting.
		proxyRequest.Header.Del("Accept-Encoding")
	}

	anthropicBetaHeader := proxyRequest.Header.Get("anthropic-beta")
	if anthropicBetaHeader != "" {
		proxyRequest.Header.Set("anthropic-beta", anthropicBetaOAuthValue+","+anthropicBetaHeader)
	} else {
		proxyRequest.Header.Set("anthropic-beta", anthropicBetaOAuthValue)
	}

	for key, values := range s.httpHeaders {
		proxyRequest.Header.Del(key)
		proxyRequest.Header[key] = values
	}

	proxyRequest.Header.Set("Authorization", "Bearer "+accessToken)

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

	if s.usageTracker != nil && response.StatusCode == http.StatusOK {
		s.handleResponseWithTracking(w, response, requestModel, anthropicBetaHeader, messagesCount, username)
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

func (s *Service) handleResponseWithTracking(writer http.ResponseWriter, response *http.Response, requestModel string, anthropicBetaHeader string, messagesCount int, username string) {
	weeklyCycleHint := extractWeeklyCycleHint(response.Header)
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	isStreaming := err == nil && mediaType == "text/event-stream"

	if !isStreaming {
		capture := &boundedCapture{limit: maxTrackedResponseBytes}
		_, copyErr := io.CopyBuffer(io.MultiWriter(writer, capture), response.Body, make([]byte, buf.BufferSize))
		if copyErr != nil {
			s.logger.Error("stream response body: ", copyErr)
			return
		}
		if capture.overflow {
			s.logger.Warn("CCM response exceeded usage-tracking limit; response was proxied without accounting")
			return
		}

		var message anthropic.Message
		var usage anthropic.Usage
		var responseModel string
		if json.Unmarshal(capture.data, &message) == nil {
			responseModel = string(message.Model)
			usage = message.Usage
		}
		if responseModel == "" {
			responseModel = requestModel
		}

		if usage.InputTokens > 0 || usage.OutputTokens > 0 {
			if responseModel != "" {
				totalInputTokens := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
				contextWindow := detectContextWindow(anthropicBetaHeader, totalInputTokens)
				if usageErr := s.usageTracker.AddUsageWithCycleHint(
					responseModel,
					contextWindow,
					messagesCount,
					usage.InputTokens,
					usage.OutputTokens,
					usage.CacheReadInputTokens,
					usage.CacheCreationInputTokens,
					usage.CacheCreation.Ephemeral5mInputTokens,
					usage.CacheCreation.Ephemeral1hInputTokens,
					username,
					time.Now(),
					weeklyCycleHint,
				); usageErr != nil {
					s.logger.Warn("record CCM usage: ", usageErr)
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

	var accumulatedUsage anthropic.Usage
	var responseModel string
	parser := &boundedSSEParser{}
	handleLine := func(line []byte) {
		line = bytes.TrimSpace(line)
		eventData, isData := bytes.CutPrefix(line, []byte("data: "))
		if !isData || bytes.Equal(eventData, []byte("[DONE]")) {
			return
		}
		var event anthropic.MessageStreamEventUnion
		if json.Unmarshal(eventData, &event) != nil {
			return
		}
		switch event.Type {
		case "message_start":
			messageStart := event.AsMessageStart()
			if messageStart.Message.Model != "" {
				responseModel = string(messageStart.Message.Model)
			}
			if messageStart.Message.Usage.InputTokens > 0 {
				accumulatedUsage.InputTokens = messageStart.Message.Usage.InputTokens
				accumulatedUsage.CacheReadInputTokens = messageStart.Message.Usage.CacheReadInputTokens
				accumulatedUsage.CacheCreationInputTokens = messageStart.Message.Usage.CacheCreationInputTokens
				accumulatedUsage.CacheCreation.Ephemeral5mInputTokens = messageStart.Message.Usage.CacheCreation.Ephemeral5mInputTokens
				accumulatedUsage.CacheCreation.Ephemeral1hInputTokens = messageStart.Message.Usage.CacheCreation.Ephemeral1hInputTokens
			}
		case "message_delta":
			messageDelta := event.AsMessageDelta()
			if messageDelta.Usage.OutputTokens > 0 {
				accumulatedUsage.OutputTokens = messageDelta.Usage.OutputTokens
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
			s.logger.Warn("CCM SSE event exceeded usage-tracking limit; stream was proxied without accounting")
			return
		}
		if responseModel == "" {
			responseModel = requestModel
		}
		if (accumulatedUsage.InputTokens > 0 || accumulatedUsage.OutputTokens > 0) && responseModel != "" {
			totalInputTokens := accumulatedUsage.InputTokens + accumulatedUsage.CacheCreationInputTokens + accumulatedUsage.CacheReadInputTokens
			contextWindow := detectContextWindow(anthropicBetaHeader, totalInputTokens)
			if usageErr := s.usageTracker.AddUsageWithCycleHint(
				responseModel,
				contextWindow,
				messagesCount,
				accumulatedUsage.InputTokens,
				accumulatedUsage.OutputTokens,
				accumulatedUsage.CacheReadInputTokens,
				accumulatedUsage.CacheCreationInputTokens,
				accumulatedUsage.CacheCreation.Ephemeral5mInputTokens,
				accumulatedUsage.CacheCreation.Ephemeral1hInputTokens,
				username,
				time.Now(),
				weeklyCycleHint,
			); usageErr != nil {
				s.logger.Warn("record CCM usage: ", usageErr)
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
	err := common.Close(
		common.PtrOrNil(s.httpServer),
		common.PtrOrNil(s.listener),
		s.tlsConfig,
	)

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
