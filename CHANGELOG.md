# Changelog

## [0.1.0] - 2026-11-17

### Added

- Run commands with a deadline on Linux and macOS from one Go executable.
- Install with `go install github.com/botforge-pro/time-limit@v0.1.0`, then run
  `time-limit 560 make test-run` for a 560-second command deadline.
- Clean up descendants across process groups after timeout, cancellation,
  or normal command exit. Send TERM, then KILL after two seconds by default.
- Use two supervisors so cleanup continues if either one is killed.
- Forward INT, TERM and HUP and directly inherit stdin, stdout and stderr.
  Preserve command exit status after successful cleanup; return 124 on timeout
  or 125 when supervision or cleanup fails.
