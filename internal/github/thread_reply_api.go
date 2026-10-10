package github

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v84/github"
)

func (g ghClient) ReplyToReviewComment(ctx stdctx.Context, owner, repo string, number int, commentID int64, body string) (string, error) {
	c, _, err := g.c.PullRequests.CreateCommentInReplyTo(ctx, owner, repo, number, body, commentID)
	if err != nil {
		return "", err
	}
	return c.GetHTMLURL(), nil
}

func (g ghClient) EditReviewComment(ctx stdctx.Context, owner, repo string, commentID int64, body string) error {
	_, _, err := g.c.PullRequests.EditComment(ctx, owner, repo, commentID, &gh.PullRequestComment{Body: gh.Ptr(body)})
	return err
}

func (g ghClient) ResolveReviewThread(ctx stdctx.Context, threadID string) error {
	token := strings.TrimSpace(g.token)
	if token == "" || strings.TrimSpace(threadID) == "" {
		return fmt.Errorf("github: cannot resolve review thread")
	}
	const query = `mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }`
	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(map[string]any{
		"query":     query,
		"variables": map[string]any{"id": threadID},
	}); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", &payload)
	if err != nil {
		return err
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
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github graphql resolve thread: %s", resp.Status)
	}
	var decoded struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	if len(decoded.Errors) > 0 {
		return fmt.Errorf("github graphql resolve thread: %s", decoded.Errors[0].Message)
	}
	return nil
}

func (g ghClient) PullRequestCommitSHAs(ctx stdctx.Context, owner, repo string, number int) ([]string, error) {
	opts := &gh.ListOptions{PerPage: 100}
	var shas []string
	for page := 0; page < maxConvPages; page++ {
		commits, resp, err := g.c.PullRequests.ListCommits(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, err
		}
		for _, c := range commits {
			if sha := c.GetSHA(); sha != "" {
				shas = append(shas, sha)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return shas, nil
		}
		opts.Page = resp.NextPage
	}
	return nil, fmt.Errorf("github: pull request commit list truncated")
}

func (g ghClient) CommitFilePatch(ctx stdctx.Context, owner, repo, sha, path string) (bool, string, error) {
	opts := &gh.ListOptions{PerPage: 100}
	for page := 0; page < 30; page++ {
		commit, resp, err := g.c.Repositories.GetCommit(ctx, owner, repo, sha, opts)
		if err != nil {
			return false, "", err
		}
		if commit != nil {
			for _, f := range commit.Files {
				if f.GetFilename() == path {
					return true, f.GetPatch(), nil
				}
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return false, "", nil
		}
		opts.Page = resp.NextPage
	}
	return false, "", fmt.Errorf("github: commit file list truncated")
}
