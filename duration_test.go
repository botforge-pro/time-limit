package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDurationAcceptsPositiveSecondsOrGoDuration(t *testing.T) {
	for _, value := range []string{
		"600",
		"600s",
		"10m",
	} {
		duration, err := parseDuration(value)
		assert.NoError(t, err)
		assert.Equal(t, 10*time.Minute, duration)
	}
}

func TestDurationRejectsNonPositiveAndOverflowingValues(t *testing.T) {
	for _, value := range []string{
		"0",
		"-1",
		"0s",
		"-1m",
		"9223372036854775807",
		"9223372036854775807h",
		"2562047h47m16.854775807s",
		"tomorrow",
	} {
		_, err := parseDuration(value)
		assert.Error(t, err, value)
	}
}
