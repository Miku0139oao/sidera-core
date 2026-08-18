package box_test

import (
	"context"
	"errors"
	"testing"

	box "github.com/Miku0139oao/sidera-core"
	"github.com/Miku0139oao/sidera-core/adapter"
	boxService "github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/include"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

func TestRuntimeCommitRunsAfterCompleteStart(t *testing.T) {
	var events []string
	instance := newRuntimeCommitTestBox(t, &events, func() (func() error, error) {
		events = append(events, "snapshot")
		return nil, nil
	})
	require.NoError(t, instance.Start())
	require.Equal(t, []string{"started", "validate", "snapshot", "commit", "activate"}, events)
	require.NoError(t, instance.Close())
}

func TestRuntimeCommitIsSkippedWhenSnapshotFails(t *testing.T) {
	var events []string
	expected := errors.New("snapshot failed")
	instance := newRuntimeCommitTestBox(t, &events, func() (func() error, error) {
		events = append(events, "snapshot")
		return nil, expected
	})
	require.ErrorIs(t, instance.Start(), expected)
	require.Equal(t, []string{"started", "validate", "snapshot"}, events)
}

func TestRuntimeActivationWaitsForEveryServiceValidation(t *testing.T) {
	var events []string
	expected := errors.New("not ready")
	instance := newRuntimeCommitTestBoxWithServices(t, &events, func() (func() error, error) {
		events = append(events, "snapshot")
		return nil, nil
	}, []runtimeCommitTestDefinition{{tag: "first"}, {tag: "second", validationErr: expected}})
	require.ErrorIs(t, instance.Start(), expected)
	require.Equal(t, []string{
		"started:first", "started:second", "validate:first", "validate:second",
	}, events)
}

func TestRuntimeCommitRollsBackEarlierCommits(t *testing.T) {
	var events []string
	expected := errors.New("commit failed")
	instance := newRuntimeCommitTestBoxWithServices(t, &events, func() (func() error, error) {
		events = append(events, "snapshot")
		return func() error {
			events = append(events, "rollback:snapshot")
			return nil
		}, nil
	}, []runtimeCommitTestDefinition{{tag: "first"}, {tag: "second", commitErr: expected}})
	require.ErrorIs(t, instance.Start(), expected)
	require.Equal(t, []string{
		"started:first", "started:second", "validate:first", "validate:second", "snapshot",
		"commit:first", "commit:second", "rollback:first", "rollback:snapshot",
	}, events)
}

type runtimeCommitTestService struct {
	boxService.Adapter
	events        *[]string
	includeTag    bool
	validationErr error
	commitErr     error
}

func (s *runtimeCommitTestService) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateStarted {
		*s.events = append(*s.events, s.event("started"))
	}
	return nil
}

func (s *runtimeCommitTestService) Close() error {
	return nil
}

func (s *runtimeCommitTestService) CommitRuntime() error {
	*s.events = append(*s.events, s.event("commit"))
	return s.commitErr
}

func (s *runtimeCommitTestService) RollbackRuntime() error {
	*s.events = append(*s.events, s.event("rollback"))
	return nil
}

func (s *runtimeCommitTestService) ValidateRuntimeActivation() error {
	*s.events = append(*s.events, s.event("validate"))
	return s.validationErr
}

func (s *runtimeCommitTestService) ActivateRuntime() {
	*s.events = append(*s.events, s.event("activate"))
}

func (s *runtimeCommitTestService) event(name string) string {
	if s.includeTag {
		return name + ":" + s.Tag()
	}
	return name
}

func newRuntimeCommitTestBox(t *testing.T, events *[]string, beforeCommit func() (func() error, error)) *box.Box {
	return newRuntimeCommitTestBoxWithServices(t, events, beforeCommit, []runtimeCommitTestDefinition{{tag: "test"}})
}

type runtimeCommitTestDefinition struct {
	tag           string
	validationErr error
	commitErr     error
}

func newRuntimeCommitTestBoxWithServices(t *testing.T, events *[]string, beforeCommit func() (func() error, error), definitions []runtimeCommitTestDefinition) *box.Box {
	t.Helper()
	registry := boxService.NewRegistry()
	boxService.Register[option.StubOptions](registry, "runtime-commit-test", func(ctx context.Context, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Service, error) {
		for _, definition := range definitions {
			if definition.tag == tag {
				return &runtimeCommitTestService{
					Adapter: boxService.NewAdapter("runtime-commit-test", tag), events: events,
					includeTag: len(definitions) > 1, validationErr: definition.validationErr, commitErr: definition.commitErr,
				}, nil
			}
		}
		return nil, errors.New("missing test service definition")
	})
	ctx := include.Context(context.Background())
	ctx = service.ContextWith[adapter.ServiceRegistry](ctx, registry)
	ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, registry)
	services := make([]option.Service, 0, len(definitions))
	for _, definition := range definitions {
		services = append(services, option.Service{Type: "runtime-commit-test", Tag: definition.tag, Options: &option.StubOptions{}})
	}
	instance, err := box.New(box.Options{
		Context:             ctx,
		Options:             option.Options{Services: services},
		BeforeRuntimeCommit: beforeCommit,
	})
	require.NoError(t, err)
	return instance
}
