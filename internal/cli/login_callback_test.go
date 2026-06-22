package cli

import (
	stdctx "context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

// TestServeCallbackDuplicateNoDeadlock fires several concurrent callbacks at the
// loopback server. The sync.Once-guarded send must absorb the duplicates so the
// handler goroutines never block and serveCallback returns promptly.
func TestServeCallbackDuplicateNoDeadlock(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	const state = "state-123"

	type res struct {
		code string
		err  error
	}
	out := make(chan res, 1)
	go func() {
		c, e := serveCallback(stdctx.Background(), ln, state)
		out <- res{c, e}
	}()

	cb := "http://" + addr + "/auth/callback?state=" + url.QueryEscape(state) + "&code=fake-code"
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp, e := http.Get(cb); e == nil {
				resp.Body.Close()
			}
		}()
	}

	select {
	case r := <-out:
		if r.err != nil || r.code != "fake-code" {
			t.Fatalf("serveCallback = %q, err=%v", r.code, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serveCallback did not return; a duplicate callback deadlocked the handler")
	}
	wg.Wait()
}

// TestServeCallbackTimeout verifies that a context deadline (no callback arrives)
// surfaces the typed login.timeout error instead of hanging forever.
func TestServeCallbackTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := stdctx.WithTimeout(stdctx.Background(), 20*time.Millisecond)
	defer cancel()

	_, err = serveCallback(ctx, ln, "state")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) || cliErr.Code != "login.timeout" {
		t.Fatalf("err = %v, want login.timeout", err)
	}
}

// TestServeCallbackCanceledNotTimeout verifies a plain cancel (not a deadline)
// keeps the canceled-before-callback code, distinct from a timeout.
func TestServeCallbackCanceledNotTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel()

	_, err = serveCallback(ctx, ln, "state")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) || cliErr.Code != "login.exchange_failed" {
		t.Fatalf("err = %v, want login.exchange_failed", err)
	}
}

// TestAvailableProvidersFromRegistry verifies the hint is built from registry
// keys (sorted) rather than a hardcoded string.
func TestAvailableProvidersFromRegistry(t *testing.T) {
	if got := availableProviders(); got != "available: openai" {
		t.Errorf("availableProviders() = %q, want %q", got, "available: openai")
	}
}
