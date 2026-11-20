package client

import (
	"testing"
	"time"
)

func TestNewClientAppliesTheDefaultsToZeroFields(t *testing.T) { // FR-client-2
	c := NewClient(ClientConfig{Resolver: "127.0.0.1:5353"})

	if c.cfg.AttemptTimeout != time.Second {
		t.Errorf("AttemptTimeout = %v, want 1s", c.cfg.AttemptTimeout)
	}
	if c.cfg.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", c.cfg.Attempts)
	}
	if c.cfg.UDPSize != 1232 {
		t.Errorf("UDPSize = %d, want 1232", c.cfg.UDPSize)
	}
}

func TestNewClientKeepsTheFieldsThatAreSet(t *testing.T) {
	want := ClientConfig{Resolver: "127.0.0.1:5353", AttemptTimeout: 2 * time.Second, Attempts: 5, UDPSize: 4096, Recursion: true}

	if got := NewClient(want).cfg; got != want {
		t.Errorf("cfg = %+v, want %+v", got, want)
	}
}
