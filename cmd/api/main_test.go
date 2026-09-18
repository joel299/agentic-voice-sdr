package main

import "testing"

func TestServerConfiguresExplicitTimeouts(t *testing.T) {
	server := newHTTPServer(":0", nil)
	if server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatal("HTTP server timeouts must be positive")
	}
}
