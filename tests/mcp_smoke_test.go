package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMCPHandshakeSmoke drives the built miucr binary over stdio: initialize must
// answer with serverInfo and tools/list must expose review_run + review_get. It
// needs no Anthropic key (the handshake is keyless) and asserts a clean stdout.
func TestMCPHandshakeSmoke(t *testing.T) {
	bin := buildBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "mcp")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	write := func(v map[string]any) {
		b, _ := json.Marshal(v)
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	write(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "smoke", "version": "0"},
		},
	})

	reader := bufio.NewReader(stdout)
	initResp := readJSON(t, reader)
	if !strings.Contains(initResp, "serverInfo") {
		t.Fatalf("initialize response missing serverInfo: %s", initResp)
	}

	write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})

	listResp := readJSON(t, reader)
	if !strings.Contains(listResp, "review_run") || !strings.Contains(listResp, "review_get") {
		t.Fatalf("tools/list missing review_run/review_get: %s", listResp)
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "miucr")
	build := exec.Command("go", "build", "-o", bin, "./cmd/miucr")
	build.Dir = root
	build.Env = append(build.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build miucr: %v\n%s", err, out)
	}
	return bin
}

// readJSON reads newline-delimited JSON-RPC frames, skipping notifications
// (frames with no "id") until it sees a response.
func readJSON(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			t.Fatalf("read: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if err == io.EOF {
				t.Fatal("server closed stdout before responding")
			}
			continue
		}
		var probe map[string]any
		if json.Unmarshal([]byte(line), &probe) != nil {
			t.Fatalf("non-JSON on stdout (corrupt stream): %q", line)
		}
		if _, hasID := probe["id"]; hasID {
			return line
		}
		if err == io.EOF {
			t.Fatal("server closed stdout before responding")
		}
	}
}
