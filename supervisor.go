package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const cleanupBudget = 8 * time.Second

func supervise(spec commandSpec) int {
	reader, writer, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "time-limit: create control pipe:", err)
		return 125
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "time-limit:", errors.Join(err, reader.Close(), writer.Close()))
		return 125
	}
	args := []string{
		"__guardian",
		spec.limit.String(),
		spec.grace.String(),
	}
	args = append(args, spec.args...)
	guardian := exec.Command(binary, args...)
	guardian.Stdin = os.Stdin
	guardian.Stdout = os.Stdout
	guardian.Stderr = os.Stderr
	guardian.ExtraFiles = []*os.File{reader}
	guardian.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := guardian.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "time-limit: start guardian:", errors.Join(err, reader.Close(), writer.Close()))
		return 125
	}
	if err := reader.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "time-limit: close control reader:", err)
	}
	done := make(chan error, 1)
	go func() { done <- guardian.Wait() }()
	timer := time.NewTimer(spec.limit + spec.grace + 2*cleanupBudget)
	defer timer.Stop()
	status := 125
	finished := false
	overdue := false
	select {
	case err := <-done:
		finished = true
		status = guardianStatus(err)
	case received := <-signals:
		sig := received.(syscall.Signal)
		status = 128 + int(sig)
		if _, err := writer.Write([]byte{byte(sig)}); err != nil {
			fmt.Fprintln(os.Stderr, "time-limit: cancel guardian:", err)
		}
	case <-timer.C:
		overdue = true
		fmt.Fprintln(os.Stderr, "time-limit: guardian exceeded its deadline")
	}
	if err := writer.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "time-limit: close control writer:", err)
		status = 125
	}
	ctx, cancel := context.WithTimeout(context.Background(), spec.grace+cleanupBudget)
	defer cancel()
	if !finished && !overdue {
		select {
		case <-done:
			finished = true
		case <-ctx.Done():
		}
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cleanupCancel()
	if err := stopSession(cleanupCtx, guardian.Process.Pid, 0, syscall.SIGKILL); err != nil {
		fmt.Fprintln(os.Stderr, "time-limit: verify guardian cleanup:", err)
		status = 125
	}
	if !finished {
		select {
		case <-done:
		case <-time.After(time.Second):
			fmt.Fprintln(os.Stderr, "time-limit: guardian could not be reaped")
			status = 125
		}
	}
	return status
}

func guardianStatus(err error) int {
	if err == nil {
		return 0
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) && exited.ExitCode() >= 0 {
		return exited.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "time-limit: guardian exited unexpectedly:", err)
	return 125
}

type parentEvent struct {
	signal syscall.Signal
	err    error
}

func runGuardian(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "time-limit: invalid guardian invocation")
		return 125
	}
	limit, limitErr := time.ParseDuration(args[0])
	grace, graceErr := time.ParseDuration(args[1])
	if limitErr != nil || graceErr != nil || limit <= 0 || grace < 0 || grace >= cleanupBudget {
		fmt.Fprintln(os.Stderr, "time-limit: invalid guardian timing")
		return 125
	}
	parent := os.NewFile(3, "parent-control")
	if parent == nil {
		fmt.Fprintln(os.Stderr, "time-limit: guardian has no control pipe")
		return 125
	}
	info, pipeErr := parent.Stat()
	session, sessionErr := unix.Getsid(0)
	if pipeErr != nil || sessionErr != nil || info.Mode()&os.ModeNamedPipe == 0 || session != os.Getpid() {
		fmt.Fprintln(os.Stderr, "time-limit: guardian requires a control pipe and its own session")
		if err := parent.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "time-limit: close invalid control pipe:", err)
		}
		return 125
	}
	unix.CloseOnExec(3)
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	events := make(chan parentEvent, 1)
	go func() {
		var data [1]byte
		n, err := parent.Read(data[:])
		event := parentEvent{}
		if n == 1 {
			event.signal = syscall.Signal(data[0])
		} else if err != nil && !errors.Is(err, io.EOF) {
			event.err = err
		}
		events <- event
		cancel()
	}()
	status := guardCommand(ctx, events, commandSpec{
		limit: limit,
		grace: grace,
		args:  args[2:],
	}, sessionMembers)
	if err := parent.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "time-limit: close guardian control pipe:", err)
		return 125
	}
	return status
}

func guardCommand(ctx context.Context, events <-chan parentEvent, spec commandSpec, observe func(context.Context, int) ([]int, error)) int {
	session := os.Getpid()
	if _, err := observe(ctx, session); err != nil {
		if ctx.Err() != nil {
			return interruption(ctx, events).status
		}
		fmt.Fprintln(os.Stderr, "time-limit: inspect processes before command start:", err)
		return 125
	}
	select {
	case <-ctx.Done():
		return interruption(ctx, events).status
	default:
	}
	command := exec.Command(spec.args[0], spec.args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "time-limit: start command:", err)
		if errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	status := 125
	stopSignal := syscall.SIGTERM
	finished := false
	select {
	case err := <-done:
		status = commandStatus(err)
		finished = true
	case <-ctx.Done():
		cause := interruption(ctx, events)
		status = cause.status
		stopSignal = cause.signal
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), spec.grace+cleanupBudget)
	defer cancel()
	if err := stopSession(cleanupCtx, session, spec.grace, stopSignal); err != nil {
		fmt.Fprintln(os.Stderr, "time-limit: command cleanup:", err)
		return 125
	}
	if !finished {
		select {
		case <-done:
		case <-cleanupCtx.Done():
			fmt.Fprintln(os.Stderr, "time-limit: command could not be reaped")
			return 125
		}
	}
	return status
}

type stopCause struct {
	status int
	signal syscall.Signal
}

func interruption(ctx context.Context, events <-chan parentEvent) stopCause {
	cause := stopCause{
		status: 125,
		signal: syscall.SIGTERM,
	}
	select {
	case event := <-events:
		if event.err != nil {
			fmt.Fprintln(os.Stderr, "time-limit: read parent control:", event.err)
		}
		if event.signal != 0 {
			cause.status = 128 + int(event.signal)
			cause.signal = event.signal
			return cause
		}
		fmt.Fprintln(os.Stderr, "time-limit: parent exited; stopping command")
		return cause
	default:
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "time-limit: deadline exceeded; stopping command")
			cause.status = 124
		}
		return cause
	}
}

func commandStatus(err error) int {
	if err == nil {
		return 0
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		if exited.ExitCode() >= 0 {
			return exited.ExitCode()
		}
	}
	fmt.Fprintln(os.Stderr, "time-limit: wait for command:", err)
	return 125
}
