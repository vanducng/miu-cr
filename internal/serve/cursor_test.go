package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCursor_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, cursorFile)

	c := newCursor()
	c.Since = time.Date(2026, 6, 22, 5, 0, 0, 0, time.UTC)
	c.recordSeen("octocat/hello#1", "abc123")
	c.recordNotif("octocat/hello#1", "2026-06-22T05:00:00Z")
	if err := c.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := loadCursor(path, discardLog())
	if !got.Since.Equal(c.Since) {
		t.Errorf("Since = %v, want %v", got.Since, c.Since)
	}
	if got.Seen["octocat/hello#1"] != "abc123" {
		t.Errorf("Seen = %v", got.Seen)
	}
	if got.NotifSeen["octocat/hello#1"] != "2026-06-22T05:00:00Z" {
		t.Errorf("NotifSeen = %v", got.NotifSeen)
	}
}

func TestCursor_LoadMissingIsEmpty(t *testing.T) {
	got := loadCursor(filepath.Join(t.TempDir(), "nope.json"), discardLog())
	if len(got.Seen) != 0 || len(got.NotifSeen) != 0 {
		t.Errorf("missing file should load empty, got %+v", got)
	}
}

func TestCursor_LoadCorruptIsEmptyNotFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, cursorFile)
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadCursor(path, discardLog())
	if len(got.Seen) != 0 || len(got.NotifSeen) != 0 {
		t.Errorf("corrupt file should load empty, got %+v", got)
	}
}

func TestCursor_TokenNeverWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, cursorFile)
	c := newCursor()
	c.recordSeen("o/r#1", "ghp_shouldnotbehereXXXXXXXXXXXXXXXXXX")
	c.Since = time.Now()
	if err := c.save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"token", "Token", "TOKEN", "auth", "secret"} {
		if strings.Contains(string(data), field) {
			t.Errorf("cursor file contains forbidden field %q: %s", field, data)
		}
	}
}

func TestCursor_PruneDropsStale(t *testing.T) {
	c := newCursor()
	c.recordSeen("fresh/repo#1", "sha1")
	c.recordNotif("fresh/repo#1", "u1")
	c.recordSeen("stale/repo#2", "sha2")
	c.touched["stale/repo#2"] = time.Now().Add(-pruneAge - time.Hour)

	c.prune(time.Now())

	if _, ok := c.Seen["fresh/repo#1"]; !ok {
		t.Error("fresh entry should survive prune")
	}
	if _, ok := c.Seen["stale/repo#2"]; ok {
		t.Error("stale entry should be pruned")
	}
}

func TestCursor_SaveCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "miu", "cr")
	path := filepath.Join(dir, cursorFile)
	c := newCursor()
	c.Since = time.Now()
	if err := c.save(path); err != nil {
		t.Fatalf("save should MkdirAll: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("cursor file missing after save: %v", err)
	}
}
