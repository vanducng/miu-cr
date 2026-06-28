package github

import (
	stdctx "context"
	"testing"
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
