package github

import (
	stdctx "context"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
)

type reviewThreadRecordClient struct {
	recordClient
	threads []ReviewThread
}

func (c *reviewThreadRecordClient) ReviewThreads(stdctx.Context, string, string, int) ([]ReviewThread, error) {
	return c.threads, nil
}

func TestResolvedThreadFingerprintsOnlyResolvedMiucrMarkers(t *testing.T) {
	c := &reviewThreadRecordClient{threads: []ReviewThread{
		{Resolved: false, Comments: []ReviewThreadComment{{Body: "<!-- miucr:fp=1111111111111111 -->"}}},
		{Resolved: true, Comments: []ReviewThreadComment{{Body: "ok\n<!-- miucr:fp=2222222222222222 -->"}}},
		{Resolved: true, Comments: []ReviewThreadComment{{Body: "no marker"}}},
	}}

	got, err := ResolvedThreadFingerprints(stdctx.Background(), c, &PRInfo{Owner: "o", Repo: "r", Number: 1})
	if err != nil {
		t.Fatalf("ResolvedThreadFingerprints: %v", err)
	}
	if !got["2222222222222222"] {
		t.Fatalf("resolved fingerprint missing: %+v", got)
	}
	if got["1111111111111111"] {
		t.Fatalf("unresolved thread fingerprint should be ignored: %+v", got)
	}
}

func TestFilterResolvedThreadFindings(t *testing.T) {
	a := mkFinding("a.go", "low", "bug", "a()", "A")
	b := mkFinding("b.go", "high", "bug", "b()", "B")

	got := FilterResolvedThreadFindings([]engine.Finding{a, b}, map[string]bool{Fingerprint(a): true})
	if len(got) != 1 || Fingerprint(got[0]) != Fingerprint(b) {
		t.Fatalf("resolved finding should be filtered, got %+v", got)
	}
}
