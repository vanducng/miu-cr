// Package oauth resolves the cached `miucr login` credential for review time and
// refreshes it (hand-rolled JSON-body grant) when it is expiring or rejected.
// It owns the on-disk oauth.json read/write so the engine stays FS-free; the
// cli/config layer injects the provider Meta (token endpoint + backend host).
// Tokens are never logged or returned in any error string.
package oauth

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// refreshSkew refreshes proactively when the token expires within this window.
const refreshSkew = 5 * time.Minute

// refreshGroup collapses concurrent refreshes for the same provider into one
// grant. Without it, parallel reviews (serve-pool workers) each POST the same
// rotating refresh_token: the first consumes it, the rest get invalid_grant and
// surface a spurious "re-login required", and the losing SaveOAuth persists a
// stale token. Mirrors the GitHub App token_source singleflight.
var refreshGroup singleflight.Group

func dedupedRefresh(ctx stdctx.Context, meta Meta, rec config.OAuthRecord, httpClient *http.Client, now func() time.Time) (config.OAuthRecord, error) {
	v, err, _ := refreshGroup.Do(meta.Provider, func() (any, error) {
		return refresh(ctx, meta, rec, httpClient, now)
	})
	if err != nil {
		return config.OAuthRecord{}, err
	}
	return v.(config.OAuthRecord), nil
}

// Meta is the per-provider routing the resolver needs, supplied by the cli layer
// (so this package need not import the cli provider registry). It carries no
// secret, only the token endpoint, OAuth client id, and the backend host.
type Meta struct {
	Provider       string
	TokenURL       string
	ClientID       string
	BackendBaseURL string
}

// Resolved is the in-memory credential for one codex-backend review. Refresh
// forces a token refresh (used on a 401) and returns the new access token.
type Resolved struct {
	AccessToken    string
	AccountID      string
	BackendBaseURL string
	Refresh        func(ctx stdctx.Context) (string, error)
}

// Credential loads the cached record for meta.Provider, refreshes it if it is
// expiring within refreshSkew, persists any refreshed record, and returns the
// resolved access token + routing. ok=false means no usable cached credential
// for this provider (caller falls through to other auth). httpClient/now are
// test seams.
func Credential(ctx stdctx.Context, meta Meta, httpClient *http.Client, now func() time.Time) (Resolved, bool, error) {
	if now == nil {
		now = time.Now
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	rec, ok, err := config.LoadOAuth()
	if err != nil || !ok {
		return Resolved{}, false, err
	}
	if rec.Provider != meta.Provider || strings.TrimSpace(rec.AccessToken) == "" {
		return Resolved{}, false, nil
	}
	if rec.ExpiringWithin(refreshSkew, now()) {
		rec, err = dedupedRefresh(ctx, meta, rec, httpClient, now)
		if err != nil {
			return Resolved{}, false, err
		}
	}
	// mu guards rec against concurrent Refresh calls (parallel reviews can hit a
	// 401 at the same time), which read+reassign the captured record.
	var mu sync.Mutex
	return Resolved{
		AccessToken:    rec.AccessToken,
		AccountID:      rec.AccountID,
		BackendBaseURL: meta.BackendBaseURL,
		Refresh: func(ctx stdctx.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			refreshed, rerr := dedupedRefresh(ctx, meta, rec, httpClient, now)
			if rerr != nil {
				return "", rerr
			}
			rec = refreshed
			return refreshed.AccessToken, nil
		},
	}, true, nil
}

// refresh runs the hand-rolled JSON-body refresh-token grant (the provider's
// token endpoint wants JSON, which x/oauth2's form-encoded refresh can't do),
// then SaveOAuths the merged record. Errors are redacted so no token leaks.
func refresh(ctx stdctx.Context, meta Meta, rec config.OAuthRecord, httpClient *http.Client, now func() time.Time) (config.OAuthRecord, error) {
	if strings.TrimSpace(rec.RefreshToken) == "" {
		return config.OAuthRecord{}, fmt.Errorf("oauth: token expired and no refresh token cached")
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     meta.ClientID,
		"grant_type":    "refresh_token",
		"refresh_token": rec.RefreshToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenURL, strings.NewReader(string(body)))
	if err != nil {
		return config.OAuthRecord{}, fmt.Errorf("oauth: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		// Network/DNS failure reaching the token endpoint, transient: the cached
		// credential may still be valid once connectivity returns. Retry-typed so the
		// review layer surfaces it as retryable, not as a stale-credential re-login.
		return config.OAuthRecord{}, &clierr.CLIError{
			Code:    "oauth.refresh_unavailable",
			Message: config.RedactString("oauth: refresh request failed: " + err.Error()),
			Exit:    1,
			Retry:   true,
			Cause:   err,
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 5xx/429 from the token endpoint is transient; a 4xx (invalid/expired refresh
		// token) is a real rejection that needs a fresh `miucr login`.
		if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode <= 599) {
			return config.OAuthRecord{}, &clierr.CLIError{
				Code:    "oauth.refresh_unavailable",
				Message: fmt.Sprintf("oauth: refresh rejected (status %d)", resp.StatusCode),
				Exit:    1,
				Retry:   true,
			}
		}
		return config.OAuthRecord{}, fmt.Errorf("oauth: refresh rejected (status %d)", resp.StatusCode)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return config.OAuthRecord{}, fmt.Errorf("oauth: decode refresh response: %s", config.RedactString(err.Error()))
	}
	if strings.TrimSpace(tr.AccessToken) == "" {
		return config.OAuthRecord{}, fmt.Errorf("oauth: refresh response had no access_token")
	}
	rec.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		rec.RefreshToken = tr.RefreshToken
	}
	if tr.IDToken != "" {
		rec.IDToken = tr.IDToken
	}
	if tr.ExpiresIn > 0 {
		rec.ExpiresAt = now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	if err := config.SaveOAuth(rec); err != nil {
		return config.OAuthRecord{}, fmt.Errorf("oauth: persist refreshed token: %s", config.RedactString(err.Error()))
	}
	return rec, nil
}
