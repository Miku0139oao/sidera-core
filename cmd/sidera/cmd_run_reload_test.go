package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	box "github.com/Miku0139oao/sidera-core"
	"github.com/Miku0139oao/sidera-core/option"

	"github.com/stretchr/testify/require"
)

func TestReloadTeardownErrorPreventsCandidateCreation(t *testing.T) {
	expected := errors.New("teardown failed")
	createCalls := 0
	_, err := reloadInstance(
		context.Background(),
		nil,
		func() {},
		option.Options{},
		option.Options{},
		"runtime.state",
		reloadOperations{
			close: func(*box.Box, context.CancelFunc) error { return expected },
			capture: func(...option.Options) ([]dashboardFileSnapshot, error) {
				t.Fatal("snapshot attempted after failed teardown")
				return nil, nil
			},
			create: func(context.Context, option.Options, string, bool) (*box.Box, context.CancelFunc, error) {
				createCalls++
				return nil, nil, nil
			},
			restore: func([]dashboardFileSnapshot) error { return nil },
			verify:  func([]dashboardFileSnapshot) error { return nil },
		},
	)
	require.ErrorIs(t, err, expected)
	require.Zero(t, createCalls)
}

func TestReloadTerminationDuringCandidateStartupSkipsRestoration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	candidateStarted := make(chan struct{})
	result := make(chan struct {
		outcome reloadOutcome
		err     error
	}, 1)
	createCalls := 0
	restoreCalls := 0
	verifyCalls := 0
	go func() {
		outcome, err := reloadInstance(
			ctx,
			nil,
			func() {},
			option.Options{},
			option.Options{},
			"runtime.state",
			reloadOperations{
				close: func(*box.Box, context.CancelFunc) error { return nil },
				capture: func(...option.Options) ([]dashboardFileSnapshot, error) {
					return []dashboardFileSnapshot{{path: "snapshot"}}, nil
				},
				create: func(createCtx context.Context, _ option.Options, _ string, _ bool) (*box.Box, context.CancelFunc, error) {
					createCalls++
					close(candidateStarted)
					<-createCtx.Done()
					return nil, nil, createCtx.Err()
				},
				restore: func([]dashboardFileSnapshot) error {
					restoreCalls++
					return nil
				},
				verify: func([]dashboardFileSnapshot) error {
					verifyCalls++
					return nil
				},
			},
		)
		result <- struct {
			outcome reloadOutcome
			err     error
		}{outcome: outcome, err: err}
	}()
	select {
	case <-candidateStarted:
	case <-time.After(time.Second):
		t.Fatal("candidate startup did not begin")
	}
	cancel()
	select {
	case reloadResult := <-result:
		require.NoError(t, reloadResult.err)
		require.True(t, reloadResult.outcome.terminated)
	case <-time.After(time.Second):
		t.Fatal("reload did not stop after termination")
	}
	require.Equal(t, 1, createCalls)
	require.Zero(t, restoreCalls)
	require.Zero(t, verifyCalls)
}

func TestRestoreUsesRandomTemporaryPathWithoutDeletingLegacyCollision(t *testing.T) {
	setRuntimeTestContext(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "dashboard.state")
	legacyCollision := path + ".restore.tmp"
	require.NoError(t, os.WriteFile(legacyCollision, []byte("legitimate source"), 0o600))

	require.NoError(t, restoreDashboardStoreFile(dashboardFileSnapshot{
		path: path, content: []byte("restored state"), mode: 0o600, exists: true,
	}))
	restored, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte("restored state"), restored)
	collisionContent, err := os.ReadFile(legacyCollision)
	require.NoError(t, err)
	require.Equal(t, []byte("legitimate source"), collisionContent)
}
