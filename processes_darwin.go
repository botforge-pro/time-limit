package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

const darwinZombieState = 5

func snapshotProcesses() ([]processInfo, error) {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, err
	}
	result := make([]processInfo, 0, len(rows))
	for _, row := range rows {
		pid := int(row.Proc.P_pid)
		if pid <= 0 || row.Proc.P_stat == darwinZombieState {
			continue
		}
		session, err := unix.Getsid(pid)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, processInfo{
			pid:     pid,
			session: session,
		})
	}
	return result, nil
}
