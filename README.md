# time-limit

Run a command with a deadline and clean up the processes it leaves behind.
Linux and macOS are supported. The executable needs no Python, `ps`, or other
helper program at runtime.

## Install

With Go:

```sh
go install github.com/botforge-pro/time-limit@latest
```

Or download the binary for your OS and architecture from
[Releases](https://github.com/botforge-pro/time-limit/releases). Rename it to
`time-limit`, run `chmod +x time-limit`, and put it in a directory on your `PATH`.

## Use

```sh
time-limit 560 make test-run
time-limit --grace=500ms 30s ./my-command 'an argument with spaces'
```

The duration is a positive integer number of seconds or a Go duration such as
`30s` or `10m`. Options precede the duration. Arguments are passed directly;
use `sh -c '...'` when you need shell syntax. The command inherits stdin,
stdout, stderr, the environment, and the working directory.

The deadline covers preparation and command execution. Cleanup follows it:
processes receive TERM, then KILL after `--grace` (default `2s`, range `0s`
to less than `8s`). INT, TERM and HUP sent to time-limit are forwarded as the
same signal before KILL. Cleanup also runs when the command exits normally
but leaves children alive.

## What cleanup covers

The command runs in a new Unix session. Cleanup enumerates its processes,
including descendants that change process group or outlive their parent.
A separate guardian watches a pipe from the wrapper: if the wrapper dies,
including by SIGKILL, the guardian cleans up. The wrapper independently
checks and cleans the session if the guardian dies.

Cleanup covers processes with the caller's UID that remain in that session.
Commands that deliberately create a new session or change UID are outside
this boundary. This is a watchdog for cooperative build and test tools,
not isolation for hostile programs. Killing both supervisors removes the
watchdog. Kernel stalls, suspended supervisors, and machine shutdown can
prevent timely cleanup.

The command has no controlling terminal. Interactive prompts that open
`/dev/tty` will fail, and Ctrl-Z suspends the wrapper without suspending the
command or its deadline. Use this for unattended commands. Work delegated to
an existing daemon or service, such as a Gradle daemon or CoreSimulator,
is outside the session and is not stopped. Disable such daemons when the
whole workload must end with the command.

The guardian allows `--grace` plus `8s` for cleanup. The wrapper gives it up to
`--grace` plus `16s` beyond the command deadline. A failed or overdue guardian
triggers immediate KILL from the wrapper, with `8s` to verify cleanup and
`1s` to reap it. Reserve `40s` beyond the requested duration
when placing this inside a larger hard deadline. If cleanup cannot be
verified, stderr reports the failure and last observed process IDs, and
the surviving wrapper returns 125.

| Exit | Meaning |
|---|---|
| Command's exit status | Command finished and cleanup succeeded |
| 124 | Deadline exceeded |
| 125 | Supervisor or cleanup failure |
| 126 | Command could not be executed |
| 127 | Command was not found |
| 128 + signal | Command or wrapper received a signal |
| 2 | Invalid CLI arguments |

Command exit codes can overlap these reserved values. If the wrapper itself
is killed, its caller observes that signal; its guardian cannot replace the
dead wrapper's exit status.

## Development

`make build`, `make format`, `make lint`, `make test-build`, and `make test`
are the entry points. `make test TEST=TestTimeout` selects tests.
`make test-linux` runs the same process tests in Docker with cgo disabled.

`TIME_LIMIT` selects an already checked watchdog for the entire test recipe;
the binary under test is built separately into `bin/time-limit`. Do not use
the binary being tested as its own outer watchdog: a broken implementation
could otherwise skip the tests and report success. For the initial release,
an external checked watchdog must be supplied through `TIME_LIMIT`.
