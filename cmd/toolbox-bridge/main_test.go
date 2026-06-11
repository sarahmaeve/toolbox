package main

import (
	"errors"
	"flag"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExitCode: -h/--help surfaces as flag.ErrHelp from ContinueOnError
// FlagSets; it must map to exit 0, not "error: flag: help requested"
// with exit 1.
func TestExitCode(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, exitCode(nil))
	assert.Equal(t, 0, exitCode(flag.ErrHelp))
	assert.Equal(t, 0, exitCode(fmt.Errorf("parse: %w", flag.ErrHelp)))
	assert.Equal(t, 1, exitCode(errors.New("boom")))
}
