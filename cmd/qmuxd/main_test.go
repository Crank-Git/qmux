package main

import (
	"testing"
)

func TestVersionPrintsVersionLine(t *testing.T) {
	exitCode := run([]string{"qmuxd", "version"})
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestHelpPrintsHelp(t *testing.T) {
	exitCode := run([]string{"qmuxd", "help"})
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestNoArgsExitsOne(t *testing.T) {
	exitCode := run([]string{"qmuxd"})
	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
}

func TestUnknownCommandExitsOne(t *testing.T) {
	exitCode := run([]string{"qmuxd", "unknown"})
	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
}
