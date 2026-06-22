package cli

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

func seededHistoryStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	base := time.Now().UTC().Add(-72 * time.Hour)
	recs := []store.ReviewRecord{
		{Mode: "staged", Status: "done", RepoDir: "/tmp/repo", CreatedAt: base,
			Findings: []engine.Finding{{File: "a.go", Line: 1, Severity: "high", Category: "bug"}}},
		{Mode: "pr", Status: "done", Owner: "acme", Repo: "widgets", Number: 7, Provider: "anthropic",
			Model: "claude", CreatedAt: time.Now().UTC().Add(-time.Minute),
			Findings:    []engine.Finding{{File: "b.go", Line: 2, Severity: "low", Category: "style"}},
			Transcript:  []byte(`[{"turn":1,"tool":"grep"}]`),
			RawPrompt:   "review this diff",
			RawResponse: "no findings",
			Stats:       map[string]any{"files_reviewed": float64(1)}},
	}
	for i := range recs {
		if _, err := s.SaveReview(stdctx.Background(), recs[i]); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	return s
}

func runHistory(t *testing.T, st store.Store, pretty bool, args ...string) (string, error) {
	t.Helper()
	prev := historyStoreFactory
	SetHistoryStoreFactory(func(stdctx.Context) (store.Store, func(), error) { return st, func() {}, nil })
	t.Cleanup(func() { historyStoreFactory = prev })
	prevPretty := prettyOutput
	prettyOutput = pretty
	t.Cleanup(func() { prettyOutput = prevPretty })

	cmd := historyCommand(&options{output: "json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return buf.String(), err
}

func decodeEnv(t *testing.T, s string) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, s)
	}
	return env
}

func TestHistoryListAndFilters(t *testing.T) {
	st := seededHistoryStore(t)

	out, err := runHistory(t, st, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	env := decodeEnv(t, out)
	if env.Kind != "history.list" {
		t.Fatalf("kind: want history.list, got %q", env.Kind)
	}
	reviews, _ := env.Data.(map[string]any)["reviews"].([]any)
	if len(reviews) != 2 {
		t.Fatalf("want 2 reviews, got %d", len(reviews))
	}
	// newest first: the PR review (acme/widgets#7) leads.
	first := reviews[0].(map[string]any)
	if first["target"] != "acme/widgets#7" {
		t.Fatalf("newest-first broken: %v", first["target"])
	}

	// --pr filter narrows to the one PR record.
	out, err = runHistory(t, st, false, "--pr", "acme/widgets#7")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if got := len(decodeEnv(t, out).Data.(map[string]any)["reviews"].([]any)); got != 1 {
		t.Fatalf("--pr filter: want 1, got %d", got)
	}

	// --since 1d drops the 72h-old record.
	out, err = runHistory(t, st, false, "--since", "1d")
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if got := len(decodeEnv(t, out).Data.(map[string]any)["reviews"].([]any)); got != 1 {
		t.Fatalf("--since 1d: want 1, got %d", got)
	}

	// --limit caps rows.
	out, err = runHistory(t, st, false, "--limit", "1")
	if err != nil {
		t.Fatalf("limit: %v", err)
	}
	if got := len(decodeEnv(t, out).Data.(map[string]any)["reviews"].([]any)); got != 1 {
		t.Fatalf("--limit 1: want 1, got %d", got)
	}
}

func TestHistoryShowFoundAndNotFound(t *testing.T) {
	st := seededHistoryStore(t)
	rows, err := st.ListReviews(stdctx.Background(), store.ReviewFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	id := rows[0].ID

	out, err := runHistory(t, st, false, "show", id)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	env := decodeEnv(t, out)
	if env.Kind != "history.record" {
		t.Fatalf("kind: want history.record, got %q", env.Kind)
	}
	data := env.Data.(map[string]any)
	if data["id"] != id {
		t.Fatalf("id mismatch: %v", data["id"])
	}
	if data["raw_prompt"] != "review this diff" {
		t.Fatalf("raw_prompt missing: %v", data["raw_prompt"])
	}
	if data["transcript"] == nil {
		t.Fatal("transcript should be present")
	}

	_, err = runHistory(t, st, false, "show", "does-not-exist")
	if err == nil {
		t.Fatal("show unknown id must error")
	}
	var ce *CLIError
	if !errors.As(err, &ce) || ce.Code != "history.not_found" {
		t.Fatalf("want history.not_found, got %v", err)
	}
}

func TestHistoryPrune(t *testing.T) {
	st := seededHistoryStore(t)

	// no policy → typed error, no delete.
	if _, err := runHistory(t, st, false, "prune", "--yes"); err == nil {
		t.Fatal("prune without policy must error")
	}
	// policy without --yes → confirm error.
	if _, err := runHistory(t, st, false, "prune", "--keep", "1"); err == nil {
		t.Fatal("prune without --yes must error")
	}

	out, err := runHistory(t, st, false, "prune", "--keep", "1", "--yes")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	env := decodeEnv(t, out)
	if env.Kind != "history.prune" {
		t.Fatalf("kind: want history.prune, got %q", env.Kind)
	}
	if got := env.Data.(map[string]any)["deleted"]; got != float64(1) {
		t.Fatalf("deleted: want 1, got %v", got)
	}
	left, _ := st.ListReviews(stdctx.Background(), store.ReviewFilter{})
	if len(left) != 1 {
		t.Fatalf("keep 1: want 1 left, got %d", len(left))
	}
}

func TestHistoryNoSecretInOutput(t *testing.T) {
	st := seededHistoryStore(t)
	out, err := runHistory(t, st, false, "show", firstID(t, st))
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, leak := range []string{"sk-ant", "ANTHROPIC_API_KEY", "Bearer "} {
		if strings.Contains(out, leak) {
			t.Fatalf("secret-shaped token leaked: %q", leak)
		}
	}
}

func firstID(t *testing.T, st store.Store) string {
	t.Helper()
	rows, err := st.ListReviews(stdctx.Background(), store.ReviewFilter{})
	if err != nil || len(rows) == 0 {
		t.Fatalf("list: %v", err)
	}
	return rows[0].ID
}
