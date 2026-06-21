//go:build live

package github

import (
	stdctx "context"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// stableLivePR is an operator-chosen public PR used only by the -tags live
// smoke. If it closes/changes, the smoke t.Skips on fetch failure; pick a
// currently-open public PR when running the live check.
const stableLivePR = "octocat/Hello-World#1"

// TestLiveNoPATDryRunRead is a build-tagged (-tags live), NO-TOKEN read-path
// smoke: ParseRef + FetchPR + FetchIntoTempClone against a public PR with an
// anonymous client. It never posts. It t.Skips when offline / on fetch failure.
func TestLiveNoPATDryRunRead(t *testing.T) {
	ctx, cancel := stdctx.WithTimeout(stdctx.Background(), 60*time.Second)
	defer cancel()

	ref, err := ParseRef(stableLivePR)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", stableLivePR, err)
	}

	client := NewClient("") // anonymous; no PAT
	info, err := FetchPR(ctx, client, ref)
	if err != nil {
		t.Skipf("offline or PR unavailable, skipping live smoke: %v", err)
	}
	if info.HeadSHA == "" || info.BaseSHA == "" {
		t.Fatalf("FetchPR returned empty SHAs: %+v", info)
	}

	dir, cleanup, err := FetchIntoTempClone(ctx, gitcmd.New(), info, "")
	if err != nil {
		t.Skipf("non-shallow fetch failed (offline?), skipping: %v", err)
	}
	defer cleanup()

	diffs, err := DiffsForPR(ctx, gitcmd.New(), dir, info.BaseSHA, info.HeadSHA)
	if err != nil {
		t.Fatalf("DiffsForPR: %v", err)
	}
	if len(diffs) == 0 {
		t.Logf("warning: PR %s produced an empty diff (base may have advanced)", stableLivePR)
	}
}
