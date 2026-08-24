package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	boxService "github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

func TestValidationCancelsContextBeforeClose(t *testing.T) {
	setRuntimeTestContext(t)
	expected := errors.New("validation close result")
	registry := boxService.NewRegistry()
	boxService.Register[option.StubOptions](registry, "validation-context-test", func(ctx context.Context, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Service, error) {
		return &validationContextTestService{
			Adapter:  boxService.NewAdapter("validation-context-test", tag),
			ctx:      ctx,
			closeErr: expected,
		}, nil
	})
	globalCtx = service.ContextWith[adapter.ServiceRegistry](globalCtx, registry)
	globalCtx = service.ContextWith[option.ServiceOptionsRegistry](globalCtx, registry)

	result := make(chan error, 1)
	go func() {
		result <- validateConfigOptions(option.Options{Services: []option.Service{{
			Type: "validation-context-test", Tag: "test", Options: &option.StubOptions{},
		}}})
	}()
	select {
	case err := <-result:
		require.ErrorIs(t, err, expected)
	case <-time.After(time.Second):
		t.Fatal("validation Close waited on an uncancelled context")
	}
}

type validationContextTestService struct {
	boxService.Adapter
	ctx      context.Context
	closeErr error
}

func (s *validationContextTestService) Start(adapter.StartStage) error { return nil }
func (s *validationContextTestService) Close() error {
	<-s.ctx.Done()
	return s.closeErr
}
