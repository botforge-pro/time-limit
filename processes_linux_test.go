package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProcessStatHandlesNamesWithSpacesParenthesesAndNewlines(t *testing.T) {
	process, alive, err := parseProcessStat(42, "42 (name ) with\nspaces) S 1 42 17 0 0")
	assert.NoError(t, err)
	assert.True(t, alive)
	assert.Equal(t, processInfo{
		pid:     42,
		session: 17,
	}, process)
}

func TestProcessStatExcludesDeadProcessesAndRejectsUnreadableRecords(t *testing.T) {
	for _, state := range []string{
		"Z",
		"X",
	} {
		_, alive, err := parseProcessStat(42, "42 (name) "+state+" 1 42 17")
		assert.NoError(t, err)
		assert.False(t, alive)
	}
	for _, body := range []string{
		"42 name S 1 42 17",
		"42 (name) S 1",
		"42 (name) S 1 42 invalid",
	} {
		_, _, err := parseProcessStat(42, body)
		assert.Error(t, err)
	}
}
