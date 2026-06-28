package engine_test

import (
	stdctx "context"
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// fakeGate is an engine.QuotaGate double: checkErr forces a block; recorded
// captures the usage Record received.
type fakeGate struct {
	checkErr     error
	checks       int
	recorded     engine.Usage
	recordCalled bool
}

func (g *fakeGate) Check(stdctx.Context) error { g.checks++; return g.checkErr }
func (g *fakeGate) Record(_ stdctx.Context, u engine.Usage) error {
	g.recordCalled = true
	g.recorded = u
	return nil
}

func TestQuotaGateBlocksBeforeAgent(t *testing.T) {
	dir := stagedGoChange(t)
	fa := &fakeAgent{}
	gate := &fakeGate{checkErr: errors.New("quota exhausted")}
	eng := engine.New(fa, gitcmd.New())
	_, err := eng.Review(stdctx.Background(), engine.Request{
		RepoDir: dir, Gate: "high", Extensions: []string{"go"}, Quota: gate,
	})
	if err == nil {
		t.Fatal("over-quota Check should block the review")
	}
	if fa.reviewCalls != 0 {
		t.Fatalf("agent must NOT run when quota blocks (calls=%d)", fa.reviewCalls)
	}
	if gate.recordCalled {
		t.Fatal("Record must not be called when the review is blocked")
	}
}

func TestQuotaGateRecordsUsageOnSuccess(t *testing.T) {
	dir := stagedGoChange(t)
	fa := &fakeAgent{usage: engine.Usage{InputTokens: 1234, OutputTokens: 567}}
	gate := &fakeGate{}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{
		RepoDir: dir, Gate: "high", Extensions: []string{"go"}, Quota: gate,
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if gate.checks != 1 {
		t.Fatalf("Check should run exactly once, got %d", gate.checks)
	}
	if !gate.recordCalled || gate.recorded.InputTokens != 1234 || gate.recorded.OutputTokens != 567 {
		t.Fatalf("Record should receive the pass usage, got called=%v %+v", gate.recordCalled, gate.recorded)
	}
	if res.Stats["input_tokens"] != float64(1234) || res.Stats["output_tokens"] != float64(567) {
		t.Fatalf("usage should surface in stats, got in=%v out=%v", res.Stats["input_tokens"], res.Stats["output_tokens"])
	}
}
