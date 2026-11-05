package rule

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common/logger"

	"github.com/stretchr/testify/require"
)

var ruleSetLifecycleSource = []byte(`{"version":4,"rules":[{"domain":["example.com"]}]}`)

func TestRuleSetConcurrentMatchReloadCleanupClose(t *testing.T) {
	ctx := context.Background()
	local := &LocalRuleSet{ctx: ctx, tag: "local", fileFormat: C.RuleSetFormatSource}
	require.NoError(t, local.reloadRules([]option.HeadlessRule{{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultHeadlessRule{
			Domain: []string{"example.com"},
		},
	}}))
	remote := &RemoteRuleSet{
		ctx:    ctx,
		logger: logger.NOP(),
		tag:    "remote",
		options: option.RuleSet{
			Format: C.RuleSetFormatSource,
		},
	}
	require.NoError(t, remote.loadBytes(ruleSetLifecycleSource))

	testCases := []struct {
		ruleSet interface {
			Match(*adapter.InboundContext) bool
			Cleanup()
			IncRef()
			DecRef()
			Close() error
		}
		reload func() error
	}{
		{
			ruleSet: local,
			reload: func() error {
				return local.reloadRules([]option.HeadlessRule{{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultHeadlessRule{
						Domain: []string{"example.com"},
					},
				}})
			},
		},
		{ruleSet: remote, reload: func() error { return remote.loadBytes(ruleSetLifecycleSource) }},
	}
	for _, testCase := range testCases {
		ruleSet := testCase.ruleSet
		var workers sync.WaitGroup
		stop := make(chan struct{})
		for range 8 {
			workers.Go(func() {
				metadata := &adapter.InboundContext{Domain: "example.com"}
				for {
					select {
					case <-stop:
						return
					default:
						ruleSet.Match(metadata)
					}
				}
			})
		}
		for range 100 {
			require.NoError(t, testCase.reload())
			ruleSet.IncRef()
			ruleSet.Cleanup()
			ruleSet.DecRef()
			ruleSet.Cleanup()
		}
		require.NoError(t, ruleSet.Close())
		close(stop)
		workers.Wait()
		require.False(t, ruleSet.Match(&adapter.InboundContext{Domain: "example.com"}))
	}
}

func TestRemoteRuleSetRejectsPublicationAfterClose(t *testing.T) {
	ruleSet := &RemoteRuleSet{
		ctx:    context.Background(),
		logger: logger.NOP(),
		options: option.RuleSet{
			Format: C.RuleSetFormatSource,
		},
	}
	require.NoError(t, ruleSet.Close())
	require.ErrorIs(t, ruleSet.loadBytes(ruleSetLifecycleSource), net.ErrClosed)
	require.False(t, ruleSet.Match(&adapter.InboundContext{Domain: "example.com"}))
}

type updaterBlockingTransport struct {
	started chan struct{}
	exited  chan struct{}
	once    sync.Once
}

func (t *updaterBlockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.once.Do(func() { close(t.started) })
	<-request.Context().Done()
	close(t.exited)
	return nil, request.Context().Err()
}

func TestRuleSetUpdaterCloseCancelsAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := &updaterBlockingTransport{started: make(chan struct{}), exited: make(chan struct{})}
	ruleSet := &RemoteRuleSet{
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger.NOP(),
		tag:            "remote",
		url:            "https://example.invalid/rules.srs",
		updateInterval: time.Nanosecond,
		httpClient:     &http.Client{Transport: transport},
		options: option.RuleSet{
			Format: C.RuleSetFormatSource,
		},
	}
	updater := NewRuleSetUpdater(ctx, []adapter.RuleSet{ruleSet})
	require.NotNil(t, updater)
	updater.Start()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("rule-set update did not start")
	}
	require.NoError(t, updater.Close())
	select {
	case <-transport.exited:
	default:
		t.Fatal("updater Close returned before the in-flight request exited")
	}
	require.NoError(t, ruleSet.Close())
}

type updaterLateTransport struct {
	started chan struct{}
	release chan struct{}
}

func (t *updaterLateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	close(t.started)
	<-t.release
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(ruleSetLifecycleSource)),
		Request:    request,
	}, nil
}

func TestRuleSetUpdaterNeverPublishesAfterClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := &updaterLateTransport{started: make(chan struct{}), release: make(chan struct{})}
	ruleSet := &RemoteRuleSet{
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger.NOP(),
		tag:            "remote",
		url:            "https://example.invalid/rules.json",
		updateInterval: time.Nanosecond,
		httpClient:     &http.Client{Transport: transport},
		options: option.RuleSet{
			Format: C.RuleSetFormatSource,
		},
	}
	updater := NewRuleSetUpdater(ctx, []adapter.RuleSet{ruleSet})
	updater.Start()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("rule-set update did not start")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- updater.Close() }()
	select {
	case <-closeResult:
		t.Fatal("updater Close returned while the transport was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(transport.release)
	select {
	case err := <-closeResult:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("updater Close did not join the released transport")
	}
	require.False(t, ruleSet.Match(&adapter.InboundContext{Domain: "example.com"}))
	require.NoError(t, ruleSet.Close())
}

func TestRemoteRuleSetRejectsOversizedCountsAndCompressionBomb(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		var compressed bytes.Buffer
		compressed.Write([]byte{'S', 'R', 'S', C.RuleSetVersionCurrent})
		writer := zlib.NewWriter(&compressed)
		var countBuffer [binary.MaxVarintLen64]byte
		countLength := binary.PutUvarint(countBuffer[:], maxRuleSetRules+1)
		_, err := writer.Write(countBuffer[:countLength])
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		ruleSet := &RemoteRuleSet{
			ctx: context.Background(),
			options: option.RuleSet{
				Format: C.RuleSetFormatBinary,
			},
		}
		require.ErrorContains(t, ruleSet.loadBytes(compressed.Bytes()), "rule count exceeds limit")
	})

	t.Run("compressed-bomb", func(t *testing.T) {
		var compressed bytes.Buffer
		compressed.Write([]byte{'S', 'R', 'S', C.RuleSetVersionCurrent})
		writer := zlib.NewWriter(&compressed)
		require.NoError(t, binary.Write(writer, binary.BigEndian, uint8(0))) // zero root rules
		chunk := make([]byte, 32*1024)
		remaining := maxRemoteRuleSetDecompressedSize + 1
		for remaining > 0 {
			writeSize := min(len(chunk), remaining)
			_, err := writer.Write(chunk[:writeSize])
			require.NoError(t, err)
			remaining -= writeSize
		}
		require.NoError(t, writer.Close())

		ruleSet := &RemoteRuleSet{
			ctx: context.Background(),
			options: option.RuleSet{
				Format: C.RuleSetFormatBinary,
			},
		}
		require.ErrorContains(t, ruleSet.loadBytes(compressed.Bytes()), "decompressed size exceeds limit")
	})
}

func TestReadRemoteRuleSetBodyRejectsDeclaredOversize(t *testing.T) {
	response := &http.Response{
		ContentLength: maxRemoteRuleSetWireSize + 1,
		Body:          io.NopCloser(bytes.NewReader(nil)),
	}
	_, err := readRemoteRuleSetBody(response)
	require.ErrorContains(t, err, "wire size limit")
}
