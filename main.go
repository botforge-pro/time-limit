package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"time"
)

type commandSpec struct {
	limit time.Duration
	grace time.Duration
	args  []string
}

var buildVersion string

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "__guardian" {
		return runGuardian(args[1:])
	}
	flags := flag.NewFlagSet("time-limit", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	grace := flags.Duration("grace", 2*time.Second, "time between TERM and KILL, less than 8s")
	version := flags.Bool("version", false, "print version")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: time-limit [--grace DURATION] DURATION COMMAND [ARG...]")
		fmt.Fprintln(os.Stderr, "DURATION is a Go duration or an integer number of seconds.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *version {
		fmt.Fprintln(os.Stdout, programVersion())
		return 0
	}
	if flags.NArg() < 2 || *grace < 0 || *grace >= cleanupBudget {
		flags.Usage()
		return 2
	}
	limit, err := parseDuration(flags.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	command := flags.Args()[1:]
	if command[0] == "--" {
		command = command[1:]
	}
	if len(command) == 0 {
		flags.Usage()
		return 2
	}
	return supervise(commandSpec{
		limit: limit,
		grace: *grace,
		args:  command,
	})
}

func parseDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		seconds, numberErr := strconv.ParseInt(value, 10, 64)
		if numberErr == nil && seconds > 0 && seconds <= int64((1<<63-1)/time.Second) {
			duration = time.Duration(seconds) * time.Second
			err = nil
		}
	}
	if err != nil || duration <= 0 || duration > time.Duration(1<<63-1)-3*cleanupBudget {
		return 0, fmt.Errorf("duration must be positive, for example 600 or 10m")
	}
	return duration, nil
}

func programVersion() string {
	if buildVersion != "" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
