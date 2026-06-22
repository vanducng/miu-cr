package cli

import (
	"bufio"
	stdctx "context"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestAskLineCtxCancelAborts(t *testing.T) {
	// initAbort normally os.Exit(130); override so the test process survives.
	var mu sync.Mutex
	aborted := false
	orig := initAbort
	initAbort = func(io.Writer) { mu.Lock(); aborted = true; mu.Unlock() }
	defer func() { initAbort = orig }()

	pr, pw := io.Pipe() // a reader that never yields a line -> Scan blocks
	defer pw.Close()
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel() // simulate Ctrl+C: context already done

	got := askLine(ctx, bufio.NewScanner(pr), io.Discard, "Provider", "1")
	if got != "1" {
		t.Fatalf("on cancel want def %q, got %q", "1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !aborted {
		t.Fatal("expected initAbort to be invoked on a cancelled context")
	}
}

func TestAskChoiceRepromptsOnInvalid(t *testing.T) {
	in := bufio.NewScanner(strings.NewReader("99\nabc\n2\n")) // two invalid, then valid
	ask := func(prompt, def string) string { return askLine(stdctx.Background(), in, io.Discard, prompt, def) }
	if got := askChoice(ask, "Auth method", "1", "1", "2", "3"); got != "2" {
		t.Fatalf("want %q after re-prompts, got %q", "2", got)
	}
}

func TestAskChoiceBlankReturnsDefault(t *testing.T) {
	in := bufio.NewScanner(strings.NewReader("\n"))
	ask := func(prompt, def string) string { return askLine(stdctx.Background(), in, io.Discard, prompt, def) }
	if got := askChoice(ask, "Auth method", "1", "1", "2", "3"); got != "1" {
		t.Fatalf("blank should return def %q, got %q", "1", got)
	}
}
