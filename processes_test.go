package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestObservationFailureRefusesToLaunchCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	broken := func(context.Context, int) ([]int, error) {
		return nil, errors.New("process enumeration unavailable")
	}
	status := guardCommand(context.Background(), nil, commandSpec{
		limit: time.Second,
		args: []string{
			"/usr/bin/touch",
			marker,
		},
	}, broken)
	assert.Equal(t, 125, status)
	assert.NoFileExists(t, marker)
}

func TestCleanupReportsObservationFailure(t *testing.T) {
	unavailable := errors.New("process enumeration unavailable")
	broken := func(context.Context, int) ([]int, error) {
		return nil, unavailable
	}
	err := (sessionStop{id: -1}).run(context.Background(), broken)
	assert.ErrorIs(t, err, unavailable)
}

func TestCleanupDeadlineReportsLastObservedProcesses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observe := func(context.Context, int) ([]int, error) {
		cancel()
		return []int{os.Getpid()}, nil
	}
	err := (sessionStop{
		id:     -1,
		grace:  time.Second,
		signal: syscall.SIGTERM,
	}).run(ctx, observe)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, strconv.Itoa(os.Getpid()))
}
