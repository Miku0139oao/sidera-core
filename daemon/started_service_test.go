package daemon

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	coreLog "github.com/Miku0139oao/sidera-core/log"
	"google.golang.org/grpc/metadata"
)

var errStopTestStream = errors.New("stop test stream")

type boundedLogTestServer struct {
	ctx       context.Context
	access    sync.Mutex
	sendCount int
	maxBatch  int
}

func (s *boundedLogTestServer) Send(message *Log) error {
	s.access.Lock()
	defer s.access.Unlock()
	if len(message.Messages) > s.maxBatch {
		s.maxBatch = len(message.Messages)
	}
	s.sendCount++
	if s.sendCount >= 20 {
		return errStopTestStream
	}
	return nil
}

func (s *boundedLogTestServer) SetHeader(metadata.MD) error  { return nil }
func (s *boundedLogTestServer) SendHeader(metadata.MD) error { return nil }
func (s *boundedLogTestServer) SetTrailer(metadata.MD)       {}
func (s *boundedLogTestServer) Context() context.Context     { return s.ctx }
func (s *boundedLogTestServer) SendMsg(any) error            { return nil }
func (s *boundedLogTestServer) RecvMsg(any) error            { return nil }

func TestSubscribeLogBoundsEachDrainUnderContinuousProduction(t *testing.T) {
	serviceContext, cancelService := context.WithCancel(context.Background())
	defer cancelService()
	startedService := NewStartedService(ServiceOptions{Context: serviceContext})
	defer startedService.Close()

	streamContext, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	server := &boundedLogTestServer{ctx: streamContext}
	result := make(chan error, 1)
	go func() { result <- startedService.SubscribeLog(nil, server) }()

	stopProducer := make(chan struct{})
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		entry := &coreLog.Entry{Level: coreLog.LevelInfo, Message: "message"}
		for {
			select {
			case <-stopProducer:
				return
			default:
				startedService.logSubscriber.Emit(entry)
				runtime.Gosched()
			}
		}
	}()

	select {
	case err := <-result:
		if !errors.Is(err, errStopTestStream) {
			t.Fatalf("unexpected stream result: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(stopProducer)
		<-producerDone
		t.Fatal("continuous production prevented bounded sends")
	}
	close(stopProducer)
	<-producerDone

	server.access.Lock()
	maxBatch := server.maxBatch
	server.access.Unlock()
	if maxBatch > 64 {
		t.Fatalf("drained %d log entries into one message, want at most 64", maxBatch)
	}
}
