package github

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
)

type ReviewThread struct {
	Resolved bool
	Comments []ReviewThreadComment
}

type ReviewThreadComment struct {
	Body string
}

type reviewThreadsClient interface {
	ReviewThreads(ctx stdctx.Context, owner, repo string, number int) ([]ReviewThread, error)
}

func ResolvedThreadFingerprints(ctx stdctx.Context, client Client, info *PRInfo) (map[string]bool, error) {
	rtc, ok := client.(reviewThreadsClient)
	if !ok || info == nil {
		return nil, nil
	}
	threads, err := rtc.ReviewThreads(ctx, info.Owner, info.Repo, info.Number)
	if err != nil {
		return nil, err
	}
	fps := map[string]bool{}
	for _, thread := range threads {
		if !thread.Resolved {
			continue
		}
		for _, comment := range thread.Comments {
			for _, m := range fpMarkerRe.FindAllStringSubmatch(comment.Body, -1) {
				fps[m[1]] = true
			}
		}
	}
	return fps, nil
}

func FilterResolvedThreadFindings(findings []engine.Finding, resolved map[string]bool) []engine.Finding {
	if len(findings) == 0 || len(resolved) == 0 {
		return findings
	}
	out := make([]engine.Finding, 0, len(findings))
	for _, f := range findings {
		if !resolved[Fingerprint(f)] {
			out = append(out, f)
		}
	}
	if len(out) == len(findings) {
		return findings
	}
	return out
}

func (g ghClient) ReviewThreads(ctx stdctx.Context, owner, repo string, number int) ([]ReviewThread, error) {
	token := strings.TrimSpace(g.token)
	if token == "" {
		return nil, nil
	}
	const query = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $cursor) {
        pageInfo {
          hasNextPage
          endCursor
        }
        nodes {
          isResolved
          comments(first: 100) {
            nodes {
              body
            }
          }
        }
      }
    }
  }
}`
	var all []ReviewThread
	var cursor *string
	for {
		page, err := g.reviewThreadsPage(ctx, query, map[string]any{
			"owner":  owner,
			"repo":   repo,
			"number": number,
			"cursor": cursor,
		}, token)
		if err != nil {
			return nil, err
		}
		for _, node := range page.Nodes {
			thread := ReviewThread{Resolved: node.IsResolved, Comments: make([]ReviewThreadComment, 0, len(node.Comments.Nodes))}
			for _, c := range node.Comments.Nodes {
				thread.Comments = append(thread.Comments, ReviewThreadComment{Body: c.Body})
			}
			all = append(all, thread)
		}
		if !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == "" {
			break
		}
		next := page.PageInfo.EndCursor
		cursor = &next
	}
	return all, nil
}

type reviewThreadsPage struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []struct {
		IsResolved bool `json:"isResolved"`
		Comments   struct {
			Nodes []struct {
				Body string `json:"body"`
			} `json:"nodes"`
		} `json:"comments"`
	} `json:"nodes"`
}

func (g ghClient) reviewThreadsPage(ctx stdctx.Context, query string, variables map[string]any, token string) (reviewThreadsPage, error) {
	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(map[string]any{"query": query, "variables": variables}); err != nil {
		return reviewThreadsPage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", &payload)
	if err != nil {
		return reviewThreadsPage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "miu-cr")

	hc := g.hc
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return reviewThreadsPage{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return reviewThreadsPage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return reviewThreadsPage{}, fmt.Errorf("github graphql review threads: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads reviewThreadsPage `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return reviewThreadsPage{}, err
	}
	if len(decoded.Errors) > 0 {
		return reviewThreadsPage{}, fmt.Errorf("github graphql review threads: %s", decoded.Errors[0].Message)
	}
	return decoded.Data.Repository.PullRequest.ReviewThreads, nil
}
