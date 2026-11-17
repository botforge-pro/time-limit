package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type processInfo struct {
	pid     int
	session int
}

type processSnapshot struct {
	processes []processInfo
	err       error
}

func sessionMembers(ctx context.Context, session int) ([]int, error) {
	processes, err := observeProcesses(ctx)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, process := range processes {
		if process.session == session && process.pid != os.Getpid() {
			pids = append(pids, process.pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}

func observeProcesses(ctx context.Context) ([]processInfo, error) {
	done := make(chan processSnapshot, 1)
	go func() {
		processes, err := snapshotProcesses()
		done <- processSnapshot{
			processes: processes,
			err:       err,
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case snapshot := <-done:
		return snapshot.processes, snapshot.err
	}
}

func stopSession(ctx context.Context, session int, grace time.Duration, first syscall.Signal) error {
	return (sessionStop{
		id:     session,
		grace:  grace,
		signal: first,
	}).run(ctx, sessionMembers)
}

type sessionStop struct {
	id     int
	grace  time.Duration
	signal syscall.Signal
}

func (s sessionStop) run(ctx context.Context, observe func(context.Context, int) ([]int, error)) error {
	forceAt := time.Now().Add(s.grace)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var last []int
	signaled := make(map[int]bool)
	for {
		pids, err := observe(ctx, s.id)
		if err != nil {
			return fmt.Errorf("inspect session %d, last observed processes %v: %w", s.id, last, err)
		}
		if len(pids) == 0 {
			return nil
		}
		last = pids
		sig := s.signal
		if !time.Now().Before(forceAt) {
			sig = syscall.SIGKILL
		}
		var failures []error
		for _, pid := range pids {
			if sig != syscall.SIGKILL && signaled[pid] {
				continue
			}
			currentSession, err := unix.Getsid(pid)
			if errors.Is(err, unix.ESRCH) {
				continue
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("inspect process %d: %w", pid, err))
				continue
			}
			if currentSession != s.id {
				continue
			}
			if err := unix.Kill(pid, sig); err != nil && !errors.Is(err, unix.ESRCH) {
				failures = append(failures, fmt.Errorf("signal process %d: %w", pid, err))
			} else {
				signaled[pid] = true
			}
		}
		if err := errors.Join(failures...); err != nil {
			return err
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return fmt.Errorf("cleanup deadline exceeded, last observed processes %v: %w", last, ctx.Err())
		}
	}
}
