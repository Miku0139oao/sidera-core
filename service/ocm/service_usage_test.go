package ocm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/log"
)

func TestUsageSaveWorkerCoalescesAndFinalSaveJoins(t *testing.T) {
	firstSaveStarted := make(chan struct{})
	unblockSaver := make(chan struct{})
	var (
		once       sync.Once
		writesLock sync.Mutex
		writes     [][]byte
		active     atomic.Int32
		maxActive  atomic.Int32
	)
	usage := &AggregatedUsage{
		ctx:          context.Background(),
		saveInterval: time.Hour,
		saveWriter: func(data []byte) error {
			current := active.Add(1)
			for {
				old := maxActive.Load()
				if current <= old || maxActive.CompareAndSwap(old, current) {
					break
				}
			}
			defer active.Add(-1)
			writesLock.Lock()
			writes = append(writes, append([]byte(nil), data...))
			writesLock.Unlock()
			once.Do(func() {
				close(firstSaveStarted)
				<-unblockSaver
			})
			return nil
		},
	}

	if err := usage.AddUsage("gpt-test", contextWindowStandard, 1, 1, 0, serviceTierDefault, "user"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstSaveStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("save worker did not start")
	}
	for index := 0; index < 10_000; index++ {
		if err := usage.AddUsage("gpt-test", contextWindowStandard, 1, 1, 0, serviceTierDefault, "user"); err != nil {
			t.Fatal(err)
		}
	}

	closeResults := make(chan error, 2)
	go func() { closeResults <- usage.closeAndSave() }()
	go func() { closeResults <- usage.closeAndSave() }()
	select {
	case err := <-closeResults:
		t.Fatalf("close returned before blocked save completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(unblockSaver)
	for range 2 {
		select {
		case err := <-closeResults:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("close did not join save worker")
		}
	}
	if maxActive.Load() != 1 {
		t.Fatalf("concurrent saves detected: %d", maxActive.Load())
	}

	writesLock.Lock()
	defer writesLock.Unlock()
	if len(writes) != 2 {
		t.Fatalf("expected in-flight and final save, got %d", len(writes))
	}
	var final AggregatedUsageJSON
	if err := json.Unmarshal(writes[len(writes)-1], &final); err != nil {
		t.Fatal(err)
	}
	if len(final.Combinations) != 1 || final.Combinations[0].Total.RequestCount != 10_001 {
		t.Fatalf("final generation missing accepted events: %+v", final.Combinations)
	}
}

func TestUsageHistoryPrunesOldPeriodsIntoExactRollup(t *testing.T) {
	usage := &AggregatedUsage{
		ctx:          context.Background(),
		saveInterval: time.Hour,
		saveWriter:   func([]byte) error { return nil },
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	for week := 0; week < 60; week++ {
		weekStart := base.Add(time.Duration(week) * 7 * 24 * time.Hour)
		hint := &WeeklyCycleHint{WindowMinutes: 7 * 24 * 60, ResetAt: weekStart.Add(7 * 24 * time.Hour)}
		if err := usage.AddUsageWithCycleHint("gpt-test", contextWindowStandard, 1, 2, 0, serviceTierDefault, "user", weekStart, hint); err != nil {
			t.Fatal(err)
		}
	}
	usage.mutex.Lock()
	positiveWeeks := make(map[int64]struct{})
	var totalRequests int
	var rollupRequests int
	for _, combination := range usage.Combinations {
		totalRequests += combination.Total.RequestCount
		if combination.WeekStartUnix > 0 {
			positiveWeeks[combination.WeekStartUnix] = struct{}{}
		} else {
			rollupRequests += combination.Total.RequestCount
		}
	}
	retainedPeriodCount := len(positiveWeeks)
	usage.mutex.Unlock()
	if retainedPeriodCount != maxRetainedUsagePeriods {
		t.Fatalf("retained %d periods, want %d", retainedPeriodCount, maxRetainedUsagePeriods)
	}
	if totalRequests != 60 || rollupRequests != 8 {
		t.Fatalf("history totals changed: total=%d rollup=%d", totalRequests, rollupRequests)
	}
	if err := usage.closeAndSave(); err != nil {
		t.Fatal(err)
	}
}

func TestOversizedTrackedRequestIsRejected(t *testing.T) {
	service := &Service{
		logger:       log.NewNOPFactory().Logger(),
		credentials:  &oauthCredentials{APIKey: "test"},
		usageTracker: &AggregatedUsage{},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte("{}")))
	request.ContentLength = maxTrackedRequestBodyBytes + 1
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}

	if _, oversized, err := readBoundedBody(bytes.NewReader(make([]byte, 129)), 128); err != nil || !oversized {
		t.Fatalf("chunked-style overrun was not rejected: oversized=%v err=%v", oversized, err)
	}
}

func TestBoundedSSEParserDiscardsEndlessLine(t *testing.T) {
	parser := &boundedSSEParser{}
	chunk := make([]byte, 64<<10)
	for range 100 {
		parser.consume(chunk, func([]byte) { t.Fatal("unterminated line was emitted") })
		if len(parser.line) > maxTrackedSSELineBytes {
			t.Fatalf("parser retained %d bytes", len(parser.line))
		}
	}
	if !parser.overflow || !parser.discarding {
		t.Fatal("oversized SSE line was not exposed")
	}
}
