package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type commandRecord struct {
	PID     int
	Group   int
	Session int
}

type invocation struct {
	limit  string
	helper string
	args   []string
	env    []string
	stdin  string
	raw    []string
	grace  string
}

type execution struct {
	dir  string
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func startInvocation(t *testing.T, input invocation) *execution {
	t.Helper()
	binary := os.Getenv("TIME_LIMIT_TEST_BINARY")
	require.NotEmpty(t, binary, "run through make test")
	helper, err := os.Executable()
	require.NoError(t, err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	args := []string{
		"--grace=100ms",
		input.limit,
		helper,
		"-test.run=^TestCommandHelper$",
		"--",
		input.helper,
		dir,
	}
	if input.grace != "" {
		args[0] = "--grace=" + input.grace
	}
	args = append(args, input.args...)
	if input.raw != nil {
		args = input.raw
	}
	command := exec.Command(binary, args...)
	command.Stdin = strings.NewReader(input.stdin)
	command.Dir = dir
	command.Env = append(os.Environ(), "TIME_LIMIT_TEST_HELPER=1")
	command.Env = append(command.Env, input.env...)
	for _, stream := range []struct {
		name string
		set  func(*os.File)
	}{
		{
			name: "stdout",
			set:  func(file *os.File) { command.Stdout = file },
		},
		{
			name: "stderr",
			set:  func(file *os.File) { command.Stderr = file },
		},
	} {
		file, err := os.Create(filepath.Join(dir, stream.name))
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, file.Close()) })
		stream.set(file)
	}
	require.NoError(t, command.Start())
	result := &execution{
		dir:  dir,
		cmd:  command,
		done: make(chan struct{}),
	}
	go func() {
		result.err = command.Wait()
		close(result.done)
	}()
	t.Cleanup(func() {
		require.Eventually(t, func() bool {
			pids, err := invocationProcesses(dir)
			if !assert.NoError(t, err) {
				return false
			}
			for _, pid := range pids {
				err := unix.Kill(pid, unix.SIGKILL)
				assert.True(t, err == nil || errors.Is(err, unix.ESRCH), "kill helper %d: %v", pid, err)
			}
			return len(pids) == 0
		}, 5*time.Second, 20*time.Millisecond)
		select {
		case <-result.done:
		case <-time.After(5 * time.Second):
			t.Error("CLI was not reaped")
		}
	})
	return result
}

func invocationProcesses(token string) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,stat=,args=").Output()
	if err != nil {
		return nil, err
	}
	var result []int
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[1], "Z") || !strings.Contains(line, token) {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, err
		}
		result = append(result, pid)
	}
	return result, nil
}

func (e *execution) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-e.done:
	case <-time.After(40 * time.Second):
		t.Fatal("CLI did not finish")
	}
	if e.err == nil {
		return 0
	}
	var exited *exec.ExitError
	require.ErrorAs(t, e.err, &exited, e.output(t, "stderr"))
	return exited.ExitCode()
}

func (e *execution) output(t *testing.T, stream string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(e.dir, stream))
	require.NoError(t, err)
	return string(body)
}

func (e *execution) record(t *testing.T, name string) commandRecord {
	t.Helper()
	file := filepath.Join(e.dir, name+".json")
	require.Eventually(t, func() bool {
		if _, err := os.Stat(file); err == nil {
			return true
		}
		select {
		case <-e.done:
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond)
	body, err := os.ReadFile(file)
	require.NoError(t, err, e.output(t, "stderr"))
	var record commandRecord
	require.NoError(t, json.Unmarshal(body, &record))
	return record
}

func (e *execution) assertStopped(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		pids, err := invocationProcesses(e.dir)
		return assert.NoError(t, err) && len(pids) == 0
	}, 5*time.Second, 20*time.Millisecond)
}

func TestPreservesArgumentsEnvironmentDirectoryStreamsAndExitStatus(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "10s",
		helper: "exit",
		args:   []string{"two words; $(not a shell)"},
		env:    []string{"TIME_LIMIT_SAMPLE=retained"},
	})
	assert.Equal(t, 7, e.wait(t))
	assert.Equal(t, "two words; $(not a shell)\nretained\n"+e.dir+"\n", e.output(t, "stdout"))
	assert.Equal(t, "command stderr\n", e.output(t, "stderr"))
}

func TestPreservesStdinWithoutAuxiliaryProgramsOnPath(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "10s",
		helper: "stdin",
		stdin:  "input from the caller\n",
		env: []string{
			"PATH=",
		},
	})
	assert.Equal(t, 0, e.wait(t))
	assert.Equal(t, "input from the caller\n", e.output(t, "stdout"))
	assert.Empty(t, e.output(t, "stderr"))
}

func TestDeadlineDuringPreparationDoesNotLaunchCommandOrExposeArguments(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "1ns",
		helper: "exit",
		args: []string{
			"secret-command-argument",
		},
	})
	assert.Equal(t, 124, e.wait(t))
	assert.Empty(t, e.output(t, "stdout"))
	assert.NotContains(t, e.output(t, "stderr"), "secret-command-argument")
}

func TestExistingFileWithoutExecutePermissionReturns126(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-executable")
	require.NoError(t, os.WriteFile(file, []byte("not executable"), 0o600))
	e := startInvocation(t, invocation{
		raw: []string{
			"1s",
			file,
		},
	})
	assert.Equal(t, 126, e.wait(t))
}

func TestCLIRejectsInvalidArgumentsAndReportsLaunchFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		status int
	}{
		{
			name:   "missing command",
			args:   []string{"1s"},
			status: 2,
		},
		{
			name: "invalid duration",
			args: []string{
				"never",
				"/does-not-exist",
			},
			status: 2,
		},
		{
			name: "zero duration",
			args: []string{
				"0",
				"/does-not-exist",
			},
			status: 2,
		},
		{
			name: "missing executable",
			args: []string{
				"1s",
				"/time-limit-command-does-not-exist",
			},
			status: 127,
		},
		{
			name:   "help",
			args:   []string{"--help"},
			status: 0,
		},
		{
			name:   "version",
			args:   []string{"--version"},
			status: 0,
		},
		{
			name: "invalid guardian",
			args: []string{
				"__guardian",
				"1s",
				"0s",
				"/does-not-exist",
			},
			status: 125,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := startInvocation(t, invocation{raw: test.args})
			assert.Equal(t, test.status, e.wait(t), e.output(t, "stderr"))
		})
	}
}

func TestTimeoutStopsDescendantsInDifferentProcessGroups(t *testing.T) {
	started := time.Now()
	e := startInvocation(t, invocation{
		limit:  "2s",
		helper: "tree",
	})
	root := e.record(t, "root")
	child := e.record(t, "child")
	assert.Equal(t, root.Session, child.Session)
	assert.NotEqual(t, root.Group, child.Group)
	assert.Equal(t, 124, e.wait(t))
	assert.GreaterOrEqual(t, time.Since(started), 2*time.Second)
	e.assertStopped(t)
	assert.FileExists(t, filepath.Join(e.dir, "root.term"))
	assert.FileExists(t, filepath.Join(e.dir, "child.term"))
	assert.Equal(t, "x", e.output(t, "root.signals"))
	assert.Equal(t, "x", e.output(t, "child.signals"))
}

func TestGuardianSurvivesTheWrapperBeingKilled(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "1m",
		helper: "tree",
	})
	e.record(t, "root")
	e.record(t, "child")
	require.NoError(t, e.cmd.Process.Kill())
	assert.Equal(t, -1, e.wait(t))
	e.assertStopped(t)
}

func TestLongGraceStillReportsTheCommandDeadline(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "1s",
		helper: "tree",
		grace:  "7.9s",
	})
	e.record(t, "root")
	e.record(t, "child")
	assert.Equal(t, 124, e.wait(t))
	e.assertStopped(t)
	assert.Equal(t, "x", e.output(t, "root.signals"))
	assert.Equal(t, "x", e.output(t, "child.signals"))
}

func TestWrapperCleansUpIfTheGuardianIsKilled(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "1m",
		helper: "tree",
	})
	root := e.record(t, "root")
	e.record(t, "child")
	require.NoError(t, unix.Kill(root.Session, unix.SIGKILL))
	assert.Equal(t, 125, e.wait(t))
	e.assertStopped(t)
}

func TestWrapperStopsACommandWhoseGuardianIsSuspended(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "1s",
		helper: "tree",
	})
	root := e.record(t, "root")
	e.record(t, "child")
	require.NoError(t, unix.Kill(root.Session, unix.SIGSTOP))
	assert.Equal(t, 125, e.wait(t))
	e.assertStopped(t)
	assert.Contains(t, e.output(t, "stderr"), "guardian exceeded its deadline")
}

func TestCancellationStopsTheWholeCommandSession(t *testing.T) {
	for _, sig := range []syscall.Signal{
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
	} {
		t.Run(sig.String(), func(t *testing.T) {
			e := startInvocation(t, invocation{
				limit:  "1m",
				helper: "tree",
			})
			e.record(t, "root")
			e.record(t, "child")
			require.NoError(t, e.cmd.Process.Signal(sig))
			assert.Equal(t, 128+int(sig), e.wait(t))
			e.assertStopped(t)
			assert.Equal(t, strconv.Itoa(int(sig)), e.output(t, "root.term"))
			assert.Equal(t, strconv.Itoa(int(sig)), e.output(t, "child.term"))
		})
	}
}

func TestCommandExitCleansUpItsOrphanedChildren(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "1m",
		helper: "orphan",
	})
	e.record(t, "child")
	assert.Equal(t, 7, e.wait(t))
	e.assertStopped(t)
}

func TestCleanupStopsAChildBornAfterTheFirstSignal(t *testing.T) {
	e := startInvocation(t, invocation{
		limit:  "1s",
		helper: "fork-during-cleanup",
		grace:  "1s",
	})
	e.record(t, "root")
	assert.Equal(t, 124, e.wait(t))
	assert.FileExists(t, filepath.Join(e.dir, "born-during-cleanup"))
	e.assertStopped(t)
}

func TestCommandHelper(t *testing.T) {
	if os.Getenv("TIME_LIMIT_TEST_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || len(os.Args) < separator+3 {
		os.Exit(90)
	}
	args := os.Args[separator+1:]
	mode := args[0]
	dir := args[1]
	if mode == "stdin" {
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(89)
		}
		os.Exit(0)
	}
	if mode == "exit" {
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(91)
		}
		fmt.Printf("%s\n%s\n%s\n", args[2], os.Getenv("TIME_LIMIT_SAMPLE"), cwd)
		fmt.Fprintln(os.Stderr, "command stderr")
		os.Exit(7)
	}
	if mode == "tree" || mode == "orphan" {
		command := exec.Command(os.Args[0], "-test.run=^TestCommandHelper$", "--", "child", dir)
		command.Env = os.Environ()
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := command.Start(); err != nil {
			os.Exit(92)
		}
		deadline := time.After(5 * time.Second)
		tick := time.NewTicker(10 * time.Millisecond)
		for {
			if _, err := os.Stat(filepath.Join(dir, "child.json")); err == nil {
				break
			}
			select {
			case <-tick.C:
			case <-deadline:
				os.Exit(93)
			}
		}
		tick.Stop()
		if mode == "orphan" {
			os.Exit(7)
		}
	}
	name := "root"
	if mode == "child" {
		name = "child"
	}
	group, err := unix.Getpgid(0)
	if err != nil {
		os.Exit(94)
	}
	session, err := unix.Getsid(0)
	if err != nil {
		os.Exit(95)
	}
	record, err := json.Marshal(commandRecord{
		PID:     os.Getpid(),
		Group:   group,
		Session: session,
	})
	if err != nil {
		os.Exit(96)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	ready := filepath.Join(dir, name+".json")
	if err := os.WriteFile(ready+".tmp", record, 0o600); err != nil {
		os.Exit(97)
	}
	if err := os.Rename(ready+".tmp", ready); err != nil {
		os.Exit(97)
	}
	for received := range signals {
		if mode == "fork-during-cleanup" {
			command := exec.Command(os.Args[0], "-test.run=^TestCommandHelper$", "--", "child", dir)
			command.Env = os.Environ()
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := command.Start(); err != nil {
				os.Exit(98)
			}
			if err := os.WriteFile(filepath.Join(dir, "born-during-cleanup"), []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
				os.Exit(98)
			}
		}
		file, err := os.OpenFile(filepath.Join(dir, name+".signals"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(98)
		}
		_, writeErr := file.WriteString("x")
		if errors.Join(writeErr, file.Close()) != nil {
			os.Exit(98)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".term"), []byte(strconv.Itoa(int(received.(syscall.Signal)))), 0o600); err != nil {
			os.Exit(98)
		}
	}
	os.Exit(99)
}
