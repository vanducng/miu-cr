package github

import (
	stdctx "context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    PRRef
		wantErr bool
	}{
		{"url", "https://github.com/vanducng/miu-cr/pull/42", PRRef{"vanducng", "miu-cr", 42}, false},
		{"url trailing slash", "https://github.com/o/r/pull/7/", PRRef{"o", "r", 7}, false},
		{"http scheme", "http://github.com/o/r/pull/3", PRRef{"o", "r", 3}, false},
		{"short", "vanducng/miu-cr#99", PRRef{"vanducng", "miu-cr", 99}, false},
		{"whitespace short", "  vanducng/miu-cr#42  ", PRRef{"vanducng", "miu-cr", 42}, false},
		{"whitespace url", "\thttps://github.com/o/r/pull/7\n", PRRef{"o", "r", 7}, false},
		{"malformed", "not a ref", PRRef{}, true},
		{"missing number", "owner/repo#", PRRef{}, true},
		{"zero number", "owner/repo#0", PRRef{}, true},
		{"non-pull url", "https://github.com/o/r/issues/1", PRRef{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRef(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				var ce *clierr.CLIError
				if !asCLIErr(err, &ce) || ce.Code != "github.bad_pr_ref" {
					t.Fatalf("want github.bad_pr_ref, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// fakeClient implements the full Client interface; only read ops are exercised
// here. Write ops are present so the same fake serves the P2 publish tests.
type fakeClient struct {
	pr        *gh.PullRequest
	getErr    error
	pages     [][]*gh.CommitFile // one slice per page; NextPage chains them
	listErr   error
	listCalls int
}

func (f *fakeClient) GetPR(_ stdctx.Context, _, _ string, _ int) (*gh.PullRequest, error) {
	return f.pr, f.getErr
}

func (f *fakeClient) GetCommit(_ stdctx.Context, _, _, _ string) (*gh.Commit, error) {
	return nil, nil
}

func (f *fakeClient) ListFiles(_ stdctx.Context, _, _ string, _ int, opts *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	idx := 0
	if opts != nil && opts.Page > 0 {
		idx = opts.Page
	}
	f.listCalls++
	resp := &gh.Response{}
	if idx+1 < len(f.pages) {
		resp.NextPage = idx + 1
	}
	if idx >= len(f.pages) {
		return nil, resp, nil
	}
	return f.pages[idx], resp, nil
}

func (f *fakeClient) CreateReview(stdctx.Context, string, string, int, *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	return nil, nil
}
func (f *fakeClient) ListReviews(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}
func (f *fakeClient) ListReviewComments(stdctx.Context, string, string, int, *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}
func (f *fakeClient) ListIssueComments(stdctx.Context, string, string, int, *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}
func (f *fakeClient) CreateIssueComment(stdctx.Context, string, string, int, *gh.IssueComment) (*gh.IssueComment, error) {
	return nil, nil
}
func (f *fakeClient) EditIssueComment(stdctx.Context, string, string, int64, *gh.IssueComment) (*gh.IssueComment, error) {
	return nil, nil
}
func (f *fakeClient) CreateIssueReaction(stdctx.Context, string, string, int, string) (*gh.Reaction, error) {
	return &gh.Reaction{}, nil
}
func (f *fakeClient) CreateCheckRun(stdctx.Context, string, string, gh.CreateCheckRunOptions) (*gh.CheckRun, error) {
	return &gh.CheckRun{ID: gh.Ptr(int64(1))}, nil
}
func (f *fakeClient) UpdateCheckRun(stdctx.Context, string, string, int64, gh.UpdateCheckRunOptions) (*gh.CheckRun, error) {
	return &gh.CheckRun{ID: gh.Ptr(int64(1))}, nil
}
func (f *fakeClient) ListCheckRunsForRef(stdctx.Context, string, string, string, *gh.ListCheckRunsOptions) (*gh.ListCheckRunsResults, *gh.Response, error) {
	return &gh.ListCheckRunsResults{}, &gh.Response{}, nil
}

func TestFetchPR(t *testing.T) {
	ref := PRRef{Owner: "vanducng", Repo: "miu-cr", Number: 1}

	t.Run("maps SHAs base branch and paginated files (same-repo, not fork)", func(t *testing.T) {
		fc := &fakeClient{
			pr: prFixture("vanducng", "miu-cr", "headsha", "basesha", "main"),
			pages: [][]*gh.CommitFile{
				{commitFile("a.go"), commitFile("b.go")},
				{commitFile("c.go")},
			},
		}
		info, err := FetchPR(stdctx.Background(), fc, ref)
		if err != nil {
			t.Fatalf("FetchPR: %v", err)
		}
		if info.HeadSHA != "headsha" || info.BaseSHA != "basesha" || info.BaseBranch != "main" {
			t.Fatalf("bad mapping: %+v", info)
		}
		if info.HTMLBase != "https://github.com/vanducng/miu-cr" {
			t.Fatalf("HTMLBase = %q, want the base repo HTML URL", info.HTMLBase)
		}
		if info.IsFork {
			t.Error("same-repo PR must not be a fork")
		}
		if got := strings.Join(info.Files, ","); got != "a.go,b.go,c.go" {
			t.Fatalf("files = %q, want a.go,b.go,c.go (pagination)", got)
		}
		if fc.listCalls != 2 {
			t.Errorf("want 2 ListFiles calls (2 pages), got %d", fc.listCalls)
		}
	})

	t.Run("case-insensitive owner/repo is not a fork", func(t *testing.T) {
		// ref is vanducng/miu-cr; canonical casing from the API differs.
		fc := &fakeClient{pr: prFixture("Vanducng", "Miu-CR", "h", "b", "main")}
		info, err := FetchPR(stdctx.Background(), fc, ref)
		if err != nil {
			t.Fatalf("FetchPR: %v", err)
		}
		if info.IsFork {
			t.Error("casing differences must not flag a same-repo PR as a fork")
		}
	})

	t.Run("cross-repo head is a fork", func(t *testing.T) {
		fc := &fakeClient{pr: prFixture("someone-else", "miu-cr", "h", "b", "main")}
		info, err := FetchPR(stdctx.Background(), fc, ref)
		if err != nil {
			t.Fatalf("FetchPR: %v", err)
		}
		if !info.IsFork {
			t.Error("cross-repo head must be flagged as fork")
		}
	})

	t.Run("nil head repo (deleted fork) is a fork", func(t *testing.T) {
		pr := prFixture("vanducng", "miu-cr", "h", "b", "main")
		pr.Head.Repo = nil
		fc := &fakeClient{pr: pr}
		info, err := FetchPR(stdctx.Background(), fc, ref)
		if err != nil {
			t.Fatalf("FetchPR: %v", err)
		}
		if !info.IsFork {
			t.Error("nil Head.Repo must be treated as fork")
		}
	})
}

func TestFetchIntoTempCloneIsNonShallow(t *testing.T) {
	rec := &recordRunner{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 5, BaseBranch: "main", BaseSHA: "b", HeadSHA: "h"}

	dir, cleanup, err := FetchIntoTempClone(stdctx.Background(), rec, info, "")
	if err != nil {
		t.Fatalf("FetchIntoTempClone: %v", err)
	}
	defer cleanup()
	if dir == "" {
		t.Fatal("want a temp dir")
	}

	var fetch []string
	for _, c := range rec.calls {
		if len(c) > 0 && c[0] == "fetch" {
			fetch = c
		}
	}
	if fetch == nil {
		t.Fatalf("no git fetch recorded; calls=%v", rec.calls)
	}
	joined := strings.Join(fetch, " ")
	if strings.Contains(joined, "--depth") {
		t.Errorf("fetch must be NON-shallow (no --depth): %q", joined)
	}
	if !strings.Contains(joined, "main") {
		t.Errorf("fetch must include the base branch: %q", joined)
	}
	if !strings.Contains(joined, "pull/5/head") {
		t.Errorf("fetch must include pull/N/head: %q", joined)
	}
}

func TestFetchIntoTempCloneEmbedsTokenInRemoteForPrivate(t *testing.T) {
	rec := &recordRunner{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, BaseBranch: "main"}
	_, cleanup, err := FetchIntoTempClone(stdctx.Background(), rec, info, "ghp_secret")
	if err != nil {
		t.Fatalf("FetchIntoTempClone: %v", err)
	}
	defer cleanup()
	for _, c := range rec.calls {
		if len(c) > 0 && c[0] == "fetch" {
			if !strings.Contains(strings.Join(c, " "), "x-access-token:ghp_secret@github.com") {
				t.Errorf("private fetch must embed token in remote URL: %v", c)
			}
		}
	}
}

// recordRunner records git args and never touches a real repo (init/fetch are
// no-ops returning success).
type recordRunner struct{ calls [][]string }

func (r *recordRunner) Output(_ stdctx.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	return nil, nil
}

func prFixture(headOwner, headRepo, headSHA, baseSHA, baseRef string) *gh.PullRequest {
	return &gh.PullRequest{
		Head: &gh.PullRequestBranch{
			SHA: gh.Ptr(headSHA),
			Repo: &gh.Repository{
				Name:  gh.Ptr(headRepo),
				Owner: &gh.User{Login: gh.Ptr(headOwner)},
			},
		},
		Base: &gh.PullRequestBranch{
			SHA:  gh.Ptr(baseSHA),
			Ref:  gh.Ptr(baseRef),
			Repo: &gh.Repository{HTMLURL: gh.Ptr("https://github.com/vanducng/miu-cr")},
		},
	}
}

func commitFile(name string) *gh.CommitFile { return &gh.CommitFile{Filename: gh.Ptr(name)} }

func asCLIErr(err error, target **clierr.CLIError) bool {
	if ce, ok := err.(*clierr.CLIError); ok {
		*target = ce
		return true
	}
	return false
}

// ghClientAt builds the real go-github client pointed at a test server so
// FetchPR runs the genuine *gh.ErrorResponse classification path offline.
func ghClientAt(t *testing.T, srvURL string) Client {
	t.Helper()
	base, err := url.Parse(srvURL + "/")
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	c := gh.NewClient(nil)
	c.BaseURL = base
	return ghClient{c: c}
}

func TestFetchPRClassifiesAPIStatus(t *testing.T) {
	ref := PRRef{Owner: "o", Repo: "r", Number: 1}
	tests := []struct {
		name     string
		status   int
		body     string
		wantCode string
		wantHint string
		retry    bool
	}{
		{"401 auth", http.StatusUnauthorized, `{"message":"Bad credentials"}`, "github.auth", "check GITHUB_TOKEN / its repo scope", false},
		{"403 auth", http.StatusForbidden, `{"message":"Forbidden"}`, "github.auth", "check GITHUB_TOKEN / its repo scope", false},
		{"404 not found", http.StatusNotFound, `{"message":"Not Found"}`, "github.pr_not_found", "check the PR exists and the token has access", false},
		{"500 unavailable", http.StatusInternalServerError, `{"message":"oops"}`, "github.unavailable", "GitHub is unavailable — retry shortly", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			_, err := FetchPR(stdctx.Background(), ghClientAt(t, srv.URL), ref)
			if err == nil {
				t.Fatal("want error")
			}
			var ce *clierr.CLIError
			if !asCLIErr(err, &ce) {
				t.Fatalf("want *clierr.CLIError, got %T", err)
			}
			if ce.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", ce.Code, tt.wantCode)
			}
			if ce.Hint != tt.wantHint {
				t.Fatalf("hint = %q, want %q", ce.Hint, tt.wantHint)
			}
			if ce.Retry != tt.retry {
				t.Fatalf("retry = %v, want %v", ce.Retry, tt.retry)
			}
			if ce.Exit != 1 {
				t.Fatalf("exit = %d, want 1", ce.Exit)
			}
		})
	}
}

func TestFetchPRNetErrorIsUnavailable(t *testing.T) {
	// A server that's closed immediately → connection refused (a net.Error).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	_, err := FetchPR(stdctx.Background(), ghClientAt(t, closedURL), PRRef{Owner: "o", Repo: "r", Number: 1})
	if err == nil {
		t.Fatal("want error")
	}
	var ce *clierr.CLIError
	if !asCLIErr(err, &ce) {
		t.Fatalf("want *clierr.CLIError, got %T", err)
	}
	if ce.Code != "github.unavailable" || !ce.Retry {
		t.Fatalf("net error → code=%q retry=%v, want github.unavailable retry=true", ce.Code, ce.Retry)
	}
}

func TestFetchPRUnrecognizedKeepsFallback(t *testing.T) {
	// 451 is a real status we don't classify → stays the default fallback code.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnavailableForLegalReasons)
		_, _ = w.Write([]byte(`{"message":"blocked"}`))
	}))
	defer srv.Close()

	_, err := FetchPR(stdctx.Background(), ghClientAt(t, srv.URL), PRRef{Owner: "o", Repo: "r", Number: 1})
	var ce *clierr.CLIError
	if !asCLIErr(err, &ce) {
		t.Fatalf("want *clierr.CLIError, got %T", err)
	}
	if ce.Code != "github.pr_fetch_failed" {
		t.Fatalf("code = %q, want github.pr_fetch_failed (unrecognized → fallback)", ce.Code)
	}
	if ce.Retry {
		t.Error("unrecognized failure must not be retryable")
	}
}

func TestFetchPRDoesNotLeakToken(t *testing.T) {
	// A 401 body that echoes a token-shaped string must be redacted in the message.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad token ghp_AAAABBBBCCCCDDDDEEEE1234"}`))
	}))
	defer srv.Close()

	_, err := FetchPR(stdctx.Background(), ghClientAt(t, srv.URL), PRRef{Owner: "o", Repo: "r", Number: 1})
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "ghp_AAAABBBBCCCCDDDDEEEE1234") {
		t.Fatalf("token leaked into message: %q", err.Error())
	}
}

func TestParseRunsCount(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"present", ReviewMarker + "\n" + runsCountToken(3) + "\n## Code Review Summary", 3},
		{"zero", runsCountToken(0), 0},
		{"missing", ReviewMarker + "\n## Code Review Summary", 0},
		{"garbled", "<!-- miu-cr-runs:abc -->", 0},
		{"first of multiple", runsCountToken(1) + "\n" + runsCountToken(9), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRunsCount(tt.body); got != tt.want {
				t.Fatalf("parseRunsCount(%q) = %d, want %d", tt.body, got, tt.want)
			}
		})
	}
}

func TestParseReviewedCommit(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"new linked", "Last reviewed commit [`abcdef1`](<https://github.com/o/r/commit/abcdef1234567890>)", "abcdef1"},
		{"new plain", "Last reviewed commit `ABCDEF1234567890` · Posted by", "abcdef1234567890"},
		{"linked", "Reviewed commit [`abcdef1`](<https://github.com/o/r/commit/abcdef1234567890>)", "abcdef1"},
		{"plain", "Reviewed commit `ABCDEF1234567890` · Posted by", "abcdef1234567890"},
		{"missing", ReviewMarker + "\n## Code Review Summary", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseReviewedCommit(tt.body); got != tt.want {
				t.Fatalf("parseReviewedCommit(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestParsePublishedCommit(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"full", publishedToken("ABCDEF1234567890", "0123456789abcdef"), "abcdef1234567890"},
		{"short", publishedToken("abcdef1", ""), "abcdef1"},
		{"missing", ReviewMarker + "\n## Code Review Summary", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePublishedCommit(tt.body); got != tt.want {
				t.Fatalf("parsePublishedCommit(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestParsePublishedKey(t *testing.T) {
	body := publishedToken("abcdef1234567890", "0123456789abcdef")
	if got := parsePublishedKey(body); got != "0123456789abcdef" {
		t.Fatalf("parsePublishedKey = %q, want 0123456789abcdef", got)
	}
	if got := parsePublishedKey(publishedToken("abcdef1", "")); got != "" {
		t.Fatalf("legacy published marker must have empty key, got %q", got)
	}
}

func TestPriorRunsCount(t *testing.T) {
	marked := func(id int64, body string) *gh.IssueComment {
		return &gh.IssueComment{ID: gh.Ptr(id), Body: gh.Ptr(body)}
	}
	tests := []struct {
		name     string
		comments []*gh.IssueComment
		err      error
		want     int
	}{
		{"no marked comment", []*gh.IssueComment{marked(1, "just a human comment")}, nil, 0},
		{
			"token present",
			[]*gh.IssueComment{marked(5, ReviewMarker+"\n"+runsCountToken(4))},
			nil, 4,
		},
		{
			"marked but no token",
			[]*gh.IssueComment{marked(5, ReviewMarker+"\n## Code Review Summary")},
			nil, 0,
		},
		{
			"lowest-id wins on duplicates",
			[]*gh.IssueComment{
				marked(9, ReviewMarker+"\n"+runsCountToken(9)),
				marked(3, ReviewMarker+"\n"+runsCountToken(2)),
			},
			nil, 2,
		},
		{"list error degrades to 0", nil, errors.New("boom"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &convClient{issueComments: tt.comments, issueCmtErr: tt.err}
			if got := priorRunsCount(stdctx.Background(), c, convInfo()); got != tt.want {
				t.Fatalf("priorRunsCount = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestFetchPRSeedsPriorLedger proves FetchPR reads the lowest-id marked comment
// ONCE and populates BOTH the runs counter and the finding ledger from that same
// body (a higher-id duplicate is ignored).
func TestFetchPRSeedsPriorLedger(t *testing.T) {
	entries := []LedgerEntry{{FP: "aaaaaaaaaaaaaaaa", Path: "a.go", Title: "X", Status: statusOpen, Sev: "high", FirstSev: "high", OpenSHA: "aaaaaa1", FirstAt: "2026-06-26T22:00:00Z"}}
	lowest := ReviewMarker + "\n" + runsCountToken(2) + "\n" + publishedToken("abcdef1234567890", "0123456789abcdef") + "\n## Code Review Summary\n" + renderLedgerMarker(entries) + "\n<sub>Last reviewed commit [`abcdef1`](<https://github.com/vanducng/miu-cr/commit/abcdef1234567890>)</sub>"
	higher := ReviewMarker + "\n" + runsCountToken(9) + "\n" + renderLedgerMarker(nil) // higher id, must be ignored

	c := &convClient{
		fakeClient: fakeClient{pr: prFixture("vanducng", "miu-cr", "headsha", "basesha", "main")},
		issueComments: []*gh.IssueComment{
			{ID: gh.Ptr(int64(9)), Body: gh.Ptr(higher)},
			{ID: gh.Ptr(int64(3)), Body: gh.Ptr(lowest)},
		},
	}
	info, err := FetchPR(stdctx.Background(), c, PRRef{Owner: "vanducng", Repo: "miu-cr", Number: 1})
	if err != nil {
		t.Fatalf("FetchPR: %v", err)
	}
	if info.ReviewCount != 3 {
		t.Fatalf("ReviewCount must be lowest-id runs token (2)+1, got %d", info.ReviewCount)
	}
	if len(info.PriorLedger) != 1 || info.PriorLedger[0].FP != "aaaaaaaaaaaaaaaa" || info.PriorLedger[0].Status != statusOpen {
		t.Fatalf("PriorLedger must parse from the SAME lowest-id comment, got %+v", info.PriorLedger)
	}
	if info.PriorSummaryHeadSHA != "abcdef1" {
		t.Fatalf("PriorSummaryHeadSHA = %q, want abcdef1", info.PriorSummaryHeadSHA)
	}
	if info.PriorPublishedHeadSHA != "abcdef1234567890" {
		t.Fatalf("PriorPublishedHeadSHA = %q, want abcdef1234567890", info.PriorPublishedHeadSHA)
	}
	if info.PriorPublishedKey != "0123456789abcdef" {
		t.Fatalf("PriorPublishedKey = %q, want 0123456789abcdef", info.PriorPublishedKey)
	}
}

// TestFetchPriorSummariesStripsLedgerMarker: the multi-KB base64 ledger payload
// must NOT be injected into the --conversation USER turn (it would displace the
// shared byte budget); the prose survives.
func TestFetchPriorSummariesStripsLedgerMarker(t *testing.T) {
	marker := renderLedgerMarker([]LedgerEntry{{FP: "aaaaaaaaaaaaaaaa", Path: "a.go", Status: statusOpen, Sev: "high", FirstSev: "high", OpenSHA: "aaaaaa1"}})
	body := ReviewMarker + "\n## Code Review Summary\n\nwalkthrough prose\n" + marker
	c := &convClient{issueComments: []*gh.IssueComment{{ID: gh.Ptr(int64(1)), Body: gh.Ptr(body)}}}

	out := fetchPriorSummaries(stdctx.Background(), c, convInfo())
	if strings.Contains(out, ledgerPrefix) {
		t.Fatalf("ledger marker must be stripped from the conversation injection:\n%s", out)
	}
	if !strings.Contains(out, "walkthrough prose") {
		t.Fatalf("summary prose must survive the strip:\n%s", out)
	}
}

// TestReviewCountIncrementChain exercises the TRUE render-path increment that the
// storeless round-trip test misses: feed a prior runs token through FetchPR (read
// + the +1) and RenderSummaryFull (display + re-write the token), then re-parse the
// rendered token to confirm it advances by exactly one each run and the displayed N
// matches. Guards the "Reviews(N) increments not double-counts" invariant.
func TestReviewCountIncrementChain(t *testing.T) {
	ref := PRRef{Owner: "vanducng", Repo: "miu-cr", Number: 1}
	prior := -1 // -1 => no prior comment (first run); >=0 => stored runs token
	for run := 1; run <= 3; run++ {
		var comments []*gh.IssueComment
		if prior >= 0 {
			comments = []*gh.IssueComment{
				{ID: gh.Ptr(int64(5)), Body: gh.Ptr(ReviewMarker + "\n" + runsCountToken(prior))},
			}
		}
		c := &convClient{
			fakeClient:    fakeClient{pr: prFixture("vanducng", "miu-cr", "headsha", "basesha", "main")},
			issueComments: comments,
		}
		info, err := FetchPR(stdctx.Background(), c, ref)
		if err != nil {
			t.Fatalf("run %d FetchPR: %v", run, err)
		}
		if info.ReviewCount != run {
			t.Fatalf("run %d: ReviewCount = %d, want %d (prior+1, not stuck/off-by-one)", run, info.ReviewCount, run)
		}
		out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{})
		if !strings.Contains(out, fmt.Sprintf("Review attempts: %d", run)) {
			t.Fatalf("run %d: displayed N must equal %d:\n%s", run, run, out)
		}
		if got := parseRunsCount(out); got != run {
			t.Fatalf("run %d: rendered token = %d, want %d (must feed next run as %d)", run, got, run, run+1)
		}
		prior = parseRunsCount(out) // next run reads what this run wrote
	}
}
