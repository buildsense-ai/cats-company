package main

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// The diagnostics listener is the only way to take a heap profile of the
// running server, so it needs to keep working: without it a slow memory
// regression can only be inferred from RSS.
func TestDiagnosticsServerServesHeapProfileWhenEnabled(t *testing.T) {
	// startDiagnosticsServer binds its own socket, so the port has to be known
	// before the call: reserve a free loopback port and hand it over.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}

	t.Setenv("CATS_DIAGNOSTICS_ADDR", addr)
	startDiagnosticsServer()

	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/debug/pprof/heap?debug=1")
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("heap profile status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		return
	}
	t.Fatalf("diagnostics listener never answered on %s: %v", addr, lastErr)
}

func TestDiagnosticsServerStaysOffWithoutAddr(t *testing.T) {
	t.Setenv("CATS_DIAGNOSTICS_ADDR", "")

	done := make(chan struct{})
	go func() {
		startDiagnosticsServer()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startDiagnosticsServer did not return when CATS_DIAGNOSTICS_ADDR is empty")
	}
}
