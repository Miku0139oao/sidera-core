package transport

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
)

const (
	reuseStateUnknown int32 = iota
	reuseStateProbing
	reuseStateSupported
	reuseStateUnsupported
)

const (
	reuseProbeTimeout       = 5 * time.Second
	reuseProbeRetryInterval = time.Minute
	reuseDemoteFailureLimit = 3

	reuseProbeQueryIdA uint16 = 1
	reuseProbeQueryIdB uint16 = 2
)

type queryMultiplexerOptions struct {
	dial           func(ctx context.Context) (net.Conn, error)
	write          func(conn net.Conn, message *mDNS.Msg, queryId uint16) error
	readNext       func(conn net.Conn) (*mDNS.Msg, error)
	retryReadError bool
	probeReuse     bool
}

type queryMultiplexer struct {
	options    queryMultiplexerOptions
	connection *ConnPool[*multiplexConn]

	lifecycleAccess sync.Mutex
	generation      *queryMultiplexerGeneration
	closed          bool
	closeDone       chan struct{}
	closeErr        error

	queryAccess sync.Mutex
	queries     map[uint16]*pendingQuery

	reuseState     atomic.Int32
	demoteFailures atomic.Int32

	probeAccess   sync.Mutex
	probeEpoch    uint32
	lastProbeTime time.Time
}

type queryMultiplexerGeneration struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	workers sync.WaitGroup
}

func newQueryMultiplexerGeneration() *queryMultiplexerGeneration {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &queryMultiplexerGeneration{ctx: ctx, cancel: cancel}
}

type multiplexConn struct {
	net.Conn
	readEpoch atomic.Uint64
}

type queryMultiplexerReadError struct {
	cause error
}

func (e *queryMultiplexerReadError) Error() string {
	return e.cause.Error()
}

func (e *queryMultiplexerReadError) Unwrap() error {
	return e.cause
}

type pendingQuery struct {
	conn        *multiplexConn
	message     *mDNS.Msg
	readEpoch   uint64
	callback    func(response *mDNS.Msg, err error)
	stopContext func() bool
	stopConn    func() bool
	retryCtx    context.Context
}

func newQueryMultiplexer(options queryMultiplexerOptions) *queryMultiplexer {
	return &queryMultiplexer{
		options:    options,
		queries:    make(map[uint16]*pendingQuery),
		generation: newQueryMultiplexerGeneration(),
		closeDone:  make(chan struct{}),
		connection: NewConnPool(ConnPoolOptions[*multiplexConn]{
			Mode: ConnPoolSingle,
			IsAlive: func(conn *multiplexConn) bool {
				return conn != nil
			},
			Close: func(conn *multiplexConn, cause error) {
				conn.Close()
			},
		}),
	}
}

func (m *queryMultiplexer) Close() error {
	m.lifecycleAccess.Lock()
	if m.closed {
		closeDone := m.closeDone
		m.lifecycleAccess.Unlock()
		<-closeDone
		return m.closeErr
	}
	m.closed = true
	generation := m.generation
	m.generation = nil
	if m.options.probeReuse {
		m.probeAccess.Lock()
		m.probeEpoch++
		m.reuseState.Store(reuseStateUnknown)
		m.probeAccess.Unlock()
	}
	m.lifecycleAccess.Unlock()

	generation.cancel(net.ErrClosed)
	closeErr := m.connection.Close()
	generation.workers.Wait()

	m.lifecycleAccess.Lock()
	m.closeErr = closeErr
	close(m.closeDone)
	m.lifecycleAccess.Unlock()
	return closeErr
}

func (m *queryMultiplexer) Reset() {
	m.lifecycleAccess.Lock()
	if m.closed {
		m.lifecycleAccess.Unlock()
		return
	}
	oldGeneration := m.generation
	m.generation = newQueryMultiplexerGeneration()
	if m.options.probeReuse {
		m.probeAccess.Lock()
		m.probeEpoch++
		m.reuseState.Store(reuseStateUnknown)
		m.lastProbeTime = time.Time{}
		m.probeAccess.Unlock()
		m.demoteFailures.Store(0)
	}
	m.lifecycleAccess.Unlock()

	oldGeneration.cancel(net.ErrClosed)
	m.connection.Reset()
	oldGeneration.workers.Wait()
}

func (m *queryMultiplexer) startWorker() (*queryMultiplexerGeneration, bool) {
	m.lifecycleAccess.Lock()
	defer m.lifecycleAccess.Unlock()
	if m.closed {
		return nil, false
	}
	generation := m.generation
	generation.workers.Add(1)
	return generation, true
}

func contextWithMultiplexerGeneration(ctx context.Context, generation *queryMultiplexerGeneration) (context.Context, func()) {
	workerCtx, cancel := context.WithCancelCause(ctx)
	generationCallbackDone := make(chan struct{})
	stopGeneration := context.AfterFunc(generation.ctx, func() {
		defer close(generationCallbackDone)
		cancel(context.Cause(generation.ctx))
	})
	if cause := context.Cause(generation.ctx); cause != nil {
		cancel(cause)
	}
	return workerCtx, func() {
		if !stopGeneration() {
			<-generationCallbackDone
		}
		cancel(nil)
	}
}

func closeConnOnContext(ctx context.Context, conn net.Conn) func() {
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(callbackDone)
		conn.Close()
	})
	return func() {
		if !stop() {
			<-callbackDone
		}
	}
}

func (m *queryMultiplexer) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	done := make(chan struct{})
	var (
		response *mDNS.Msg
		err      error
	)
	m.ExchangeAsync(ctx, message, func(callbackResponse *mDNS.Msg, callbackErr error) {
		response = callbackResponse
		err = callbackErr
		close(done)
	})
	<-done
	return response, err
}

func (m *queryMultiplexer) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	m.dispatch(ctx, message, callback, true)
}

func (m *queryMultiplexer) dispatch(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error), retryReadError bool) {
	if m.options.probeReuse && m.reuseState.Load() != reuseStateSupported {
		m.maybeStartProbe(ctx, message)
		generation, started := m.startWorker()
		if !started {
			callback(nil, net.ErrClosed)
			return
		}
		go func() {
			workerCtx, cancel := contextWithMultiplexerGeneration(ctx, generation)
			response, err := m.exchangeSingle(workerCtx, message)
			cancel()
			// The callback is consumer code, not a generation worker. Releasing
			// the worker lease first lets a cancellation callback safely call Close.
			generation.workers.Done()
			callback(response, err)
		}()
		return
	}
	m.lifecycleAccess.Lock()
	closed := m.closed
	m.lifecycleAccess.Unlock()
	if closed {
		callback(nil, net.ErrClosed)
		return
	}
	go m.exchangeAsync(ctx, message, callback, retryReadError)
}

func (m *queryMultiplexer) exchangeSingle(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	conn, err := m.options.dial(ctx)
	if err != nil {
		if ctxErr := context.Cause(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	defer conn.Close()
	defer closeConnOnContext(ctx, conn)()
	queryID := mDNS.Id()
	err = m.options.write(conn, message, queryID)
	if err != nil {
		if ctxErr := context.Cause(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, E.Cause(err, "write request")
	}
	for {
		response, readErr := m.options.readNext(conn)
		if readErr != nil {
			if ctxErr := context.Cause(ctx); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, E.Cause(readErr, "read response")
		}
		if response == nil || response.Id != queryID || validateDNSResponse(message, response) != nil {
			continue
		}
		response.Id = message.Id
		return response, nil
	}
}

func (m *queryMultiplexer) maybeStartProbe(ctx context.Context, message *mDNS.Msg) {
	if len(message.Question) == 0 {
		return
	}
	m.lifecycleAccess.Lock()
	if m.closed {
		m.lifecycleAccess.Unlock()
		return
	}
	m.probeAccess.Lock()
	if m.reuseState.Load() == reuseStateProbing {
		m.probeAccess.Unlock()
		m.lifecycleAccess.Unlock()
		return
	}
	if !m.lastProbeTime.IsZero() && time.Since(m.lastProbeTime) < reuseProbeRetryInterval {
		m.probeAccess.Unlock()
		m.lifecycleAccess.Unlock()
		return
	}
	m.reuseState.Store(reuseStateProbing)
	m.lastProbeTime = time.Now()
	epoch := m.probeEpoch
	generation := m.generation
	generation.workers.Add(1)
	m.probeAccess.Unlock()
	m.lifecycleAccess.Unlock()
	go m.runReuseProbe(context.WithoutCancel(ctx), message.Question[0].Name, epoch, generation)
}

func (m *queryMultiplexer) runReuseProbe(ctx context.Context, questionName string, epoch uint32, generation *queryMultiplexerGeneration) {
	defer generation.workers.Done()
	probeCtx, cancel := contextWithMultiplexerGeneration(ctx, generation)
	supported, dialFailed := m.executeReuseProbe(probeCtx, questionName)
	cancel()

	m.lifecycleAccess.Lock()
	defer m.lifecycleAccess.Unlock()
	if m.closed || m.generation != generation {
		return
	}
	m.probeAccess.Lock()
	defer m.probeAccess.Unlock()
	if m.probeEpoch != epoch {
		return
	}
	switch {
	case supported:
		m.reuseState.Store(reuseStateSupported)
		m.demoteFailures.Store(0)
	case dialFailed:
		m.reuseState.Store(reuseStateUnknown)
	default:
		m.reuseState.Store(reuseStateUnsupported)
	}
}

func (m *queryMultiplexer) executeReuseProbe(ctx context.Context, questionName string) (supported bool, dialFailed bool) {
	probeCtx, cancel := context.WithTimeout(ctx, reuseProbeTimeout)
	defer cancel()
	conn, err := m.options.dial(probeCtx)
	if err != nil {
		return false, true
	}
	defer conn.Close()
	defer closeConnOnContext(probeCtx, conn)()
	queryA := new(mDNS.Msg)
	queryA.SetQuestion(questionName, mDNS.TypeA)
	queryAAAA := new(mDNS.Msg)
	queryAAAA.SetQuestion(questionName, mDNS.TypeAAAA)
	err = m.options.write(conn, queryA, reuseProbeQueryIdA)
	if err == nil {
		err = m.options.write(conn, queryAAAA, reuseProbeQueryIdB)
	}
	if err != nil {
		return false, false
	}
	var seenA, seenAAAA bool
	for !seenA || !seenAAAA {
		var response *mDNS.Msg
		response, err = m.options.readNext(conn)
		if err != nil {
			return false, false
		}
		if response == nil {
			continue
		}
		switch response.Id {
		case reuseProbeQueryIdA:
			seenA = seenA || validateDNSResponse(queryA, response) == nil
		case reuseProbeQueryIdB:
			seenAAAA = seenAAAA || validateDNSResponse(queryAAAA, response) == nil
		}
	}
	return true, false
}

func (m *queryMultiplexer) recordConnDeath(conn *multiplexConn) {
	if !m.options.probeReuse || m.reuseState.Load() != reuseStateSupported {
		return
	}
	if conn.readEpoch.Load() == 0 {
		return
	}
	m.queryAccess.Lock()
	var pendingOnConn int
	for _, pending := range m.queries {
		if pending.conn == conn {
			pendingOnConn++
		}
	}
	m.queryAccess.Unlock()
	if pendingOnConn == 0 {
		m.demoteFailures.Store(0)
		return
	}
	if m.demoteFailures.Add(1) < reuseDemoteFailureLimit {
		return
	}
	m.probeAccess.Lock()
	if m.reuseState.Load() == reuseStateSupported {
		m.reuseState.Store(reuseStateUnsupported)
		m.lastProbeTime = time.Now()
	}
	m.probeAccess.Unlock()
	m.demoteFailures.Store(0)
}

func (m *queryMultiplexer) exchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error), retryReadError bool) {
	for firstAttempt := true; ; firstAttempt = false {
		conn, connCtx, created, err := m.connection.AcquireShared(ctx, m.dialConn)
		if err != nil {
			callback(nil, err)
			return
		}
		if created {
			go m.recvLoop(conn)
		}
		queryId, err := m.register(ctx, connCtx, conn, message, callback, retryReadError && m.options.retryReadError && !created)
		if err != nil {
			m.connection.Release(conn, true)
			callback(nil, err)
			return
		}
		writeErr := m.options.write(conn, message, queryId)
		if writeErr == nil {
			return
		}
		pending := m.take(queryId)
		m.connection.Invalidate(conn, writeErr)
		if pending == nil {
			return
		}
		if !created && firstAttempt {
			continue
		}
		callback(nil, E.Cause(writeErr, "write request"))
		return
	}
}

func (m *queryMultiplexer) dialConn(ctx context.Context) (*multiplexConn, error) {
	conn, err := m.options.dial(ctx)
	if err != nil {
		return nil, err
	}
	return &multiplexConn{Conn: conn}, nil
}

func (m *queryMultiplexer) register(ctx context.Context, connCtx context.Context, conn *multiplexConn, message *mDNS.Msg, callback func(response *mDNS.Msg, err error), retryReadError bool) (uint16, error) {
	m.queryAccess.Lock()
	defer m.queryAccess.Unlock()
	queryId := mDNS.Id()
	for attempts := 0; ; attempts++ {
		if _, exists := m.queries[queryId]; !exists {
			break
		}
		if attempts == 1<<16-1 {
			return 0, E.New("no available query ID")
		}
		queryId++
	}
	pending := &pendingQuery{
		conn:      conn,
		message:   message,
		readEpoch: conn.readEpoch.Load(),
		callback:  callback,
	}
	if retryReadError {
		pending.retryCtx = ctx
	}
	m.queries[queryId] = pending
	pending.stopContext = context.AfterFunc(ctx, func() {
		m.completeContextDone(queryId, ctx)
	})
	pending.stopConn = context.AfterFunc(connCtx, func() {
		m.completeConnDone(queryId, connCtx)
	})
	return queryId, nil
}

func (m *queryMultiplexer) completeConnDone(queryId uint16, connCtx context.Context) {
	pending := m.take(queryId)
	if pending == nil {
		return
	}
	connErr := context.Cause(connCtx)
	_, readFailed := connErr.(*queryMultiplexerReadError)
	if pending.retryCtx != nil && readFailed {
		m.dispatch(pending.retryCtx, pending.message, pending.callback, false)
		return
	}
	pending.callback(nil, connErr)
}

func (m *queryMultiplexer) take(queryId uint16) *pendingQuery {
	m.queryAccess.Lock()
	pending, loaded := m.queries[queryId]
	if !loaded {
		m.queryAccess.Unlock()
		return nil
	}
	delete(m.queries, queryId)
	m.queryAccess.Unlock()
	pending.stopContext()
	pending.stopConn()
	return pending
}

func (m *queryMultiplexer) complete(queryId uint16, response *mDNS.Msg, err error, releaseConn bool) {
	var pending *pendingQuery
	if response != nil && err == nil {
		m.queryAccess.Lock()
		candidate := m.queries[queryId]
		if candidate == nil || validateDNSResponse(candidate.message, response) != nil {
			m.queryAccess.Unlock()
			return
		}
		delete(m.queries, queryId)
		m.queryAccess.Unlock()
		candidate.stopContext()
		candidate.stopConn()
		pending = candidate
	} else {
		pending = m.take(queryId)
		if pending == nil {
			return
		}
	}
	if releaseConn {
		m.connection.Release(pending.conn, true)
	}
	if response != nil {
		response.Id = pending.message.Id
	}
	pending.callback(response, err)
}

func validateDNSResponse(query *mDNS.Msg, response *mDNS.Msg) error {
	if !response.Response {
		return E.New("DNS response has the query bit set")
	}
	if response.Opcode != query.Opcode {
		return E.New("DNS response opcode does not match query")
	}
	if len(response.Question) == 0 && response.Rcode != mDNS.RcodeSuccess {
		return nil
	}
	if len(response.Question) != len(query.Question) {
		return E.New("DNS response question count does not match query")
	}
	for index, question := range query.Question {
		responseQuestion := response.Question[index]
		if question.Qtype != responseQuestion.Qtype || question.Qclass != responseQuestion.Qclass || !strings.EqualFold(question.Name, responseQuestion.Name) {
			return E.New("DNS response question does not match query")
		}
	}
	return nil
}

func (m *queryMultiplexer) completeContextDone(queryId uint16, ctx context.Context) {
	pending := m.take(queryId)
	if pending == nil {
		return
	}
	err := ctx.Err()
	if errors.Is(err, context.DeadlineExceeded) && pending.conn.readEpoch.Load() == pending.readEpoch {
		m.connection.Invalidate(pending.conn, err)
	} else {
		m.connection.Release(pending.conn, true)
	}
	pending.callback(nil, err)
}

func (m *queryMultiplexer) recvLoop(conn *multiplexConn) {
	for {
		message, err := m.options.readNext(conn)
		if err != nil {
			m.recordConnDeath(conn)
			m.connection.Invalidate(conn, &queryMultiplexerReadError{cause: err})
			return
		}
		conn.readEpoch.Add(1)
		if message == nil {
			continue
		}
		m.complete(message.Id, message, nil, true)
	}
}
