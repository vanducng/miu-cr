package github

import (
	stdctx "context"
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
