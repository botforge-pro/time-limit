package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func snapshotProcesses() ([]processInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	result := make([]processInfo, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		dir := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(dir)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("no process ownership for %s", dir)
		}
		if int(stat.Uid) != os.Getuid() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, "stat"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, err
		}
		process, alive, err := parseProcessStat(pid, string(body))
		if err != nil {
			return nil, err
		}
		if alive {
			result = append(result, process)
		}
	}
	return result, nil
}

func parseProcessStat(pid int, body string) (processInfo, bool, error) {
	end := strings.LastIndex(body, ")")
	if end < 0 {
		return processInfo{}, false, fmt.Errorf("invalid stat for process %d", pid)
	}
	fields := strings.Fields(body[end+1:])
	if len(fields) < 4 {
		return processInfo{}, false, fmt.Errorf("incomplete stat for process %d", pid)
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return processInfo{}, false, nil
	}
	session, err := strconv.Atoi(fields[3])
	if err != nil {
		return processInfo{}, false, err
	}
	return processInfo{
		pid:     pid,
		session: session,
	}, true, nil
}
