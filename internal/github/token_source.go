package github

import (
	stdctx "context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	gh "github.com/google/go-github/v84/github"
	"golang.org/x/sync/singleflight"
)

// ReadPrivateKeyFile reads a GitHub App private key from a PEM file at path,
// parses it (PKCS#1 or PKCS#8 RSA), and zeroes the raw PEM bytes before
// returning, the key never lingers in a buffer and is never logged.
func ReadPrivateKeyFile(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("github: read private key: %w", err)
	}
	key, perr := parsePrivateKey(raw)
	for i := range raw {
		raw[i] = 0
	}
	if perr != nil {
		return nil, perr
	}
	return key, nil
}

// TokenSource yields the bearer token NewClient/WithAuthToken authenticates with.
// staticTokenSource returns a fixed PAT (or "" anonymous), today's behavior;
// appTokenSource mints+caches a GitHub App installation token.
type TokenSource interface {
	Token(ctx stdctx.Context) (string, error)
}

// staticTokenSource is the default: it returns a fixed token unchanged, exactly
// reproducing the pre-M8 PAT/anonymous behavior ("" → anonymous client).
type staticTokenSource struct{ tok string }

// NewStaticTokenSource wraps a PAT (or "" for anonymous) as a TokenSource.
func NewStaticTokenSource(tok string) TokenSource { return staticTokenSource{tok: tok} }

func (s staticTokenSource) Token(stdctx.Context) (string, error) { return s.tok, nil }

// appExchanger is the narrow GitHub App surface appTokenSource needs: exchange an
// App JWT for an installation token. Real impl builds a JWT-authed go-github client
// per call; tests fake it so no network/real key is touched.
type appExchanger interface {
	CreateInstallationToken(ctx stdctx.Context, appJWT string, installID int64) (token string, expiry time.Time, err error)
}

// installTokenExpiryMargin refreshes ~5min before GitHub's stated expiry so an
// in-flight review never races the token going stale. GitHub installation tokens
// always live ~1h, so a token whose remaining life is below this margin at mint
// time (the busy-loop edge case) does not occur in practice; no clamp needed.
const installTokenExpiryMargin = 5 * time.Minute

// installTokenMintTimeout bounds the detached mint inside the singleflight closure
// so it can't hang forever yet outlives any single caller's request lifetime.
const installTokenMintTimeout = 30 * time.Second

// appTokenSource mints a GitHub App JWT, exchanges it for an installation token,
// and caches that token in-memory (refresh-before-expiry + single-flight). The
// installation token is never persisted, logged, or placed in the envelope.
type appTokenSource struct {
	appID     string
	installID int64
	key       *rsa.PrivateKey
	apps      appExchanger
	now       func() time.Time
	group     singleflight.Group

	mu     sync.Mutex
	cached string
	expiry time.Time
}

// NewAppTokenSource builds an installation-token source. installID must be the
// numeric installation_id (go-github needs int64); key is the parsed App private
// key (the caller zeroes the raw PEM after parse). now defaults to time.Now.
func NewAppTokenSource(appID string, installID int64, key *rsa.PrivateKey, apps appExchanger, now func() time.Time) TokenSource {
	if now == nil {
		now = time.Now
	}
	return &appTokenSource{appID: appID, installID: installID, key: key, apps: apps, now: now}
}

func (a *appTokenSource) Token(_ stdctx.Context) (string, error) {
	if tok, ok := a.cachedToken(); ok {
		return tok, nil
	}
	// Single-flight keyed by installID so a refresh thundering herd hits GitHub
	// once; concurrent callers share the one in-flight mint's result.
	key := fmt.Sprintf("%d", a.installID)
	v, err, _ := a.group.Do(key, func() (any, error) {
		if tok, ok := a.cachedToken(); ok {
			return tok, nil
		}
		// Detach from the caller's ctx: the mint is SHARED across all waiters, so
		// binding it to the first caller's request would let that caller's
		// cancellation abort the token for everyone. Use a fresh bounded ctx.
		mintCtx, cancel := stdctx.WithTimeout(stdctx.Background(), installTokenMintTimeout)
		defer cancel()
		return a.mint(mintCtx)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (a *appTokenSource) cachedToken() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cached != "" && a.now().Before(a.expiry.Add(-installTokenExpiryMargin)) {
		return a.cached, true
	}
	return "", false
}

func (a *appTokenSource) mint(ctx stdctx.Context) (string, error) {
	appJWT, err := mintAppJWT(a.key, a.appID, a.now())
	if err != nil {
		return "", err
	}
	tok, expiry, err := a.apps.CreateInstallationToken(ctx, appJWT, a.installID)
	if err != nil {
		return "", fmt.Errorf("github: exchange installation token: %w", err)
	}
	if tok == "" {
		return "", errors.New("github: empty installation token")
	}
	a.mu.Lock()
	a.cached = tok
	a.expiry = expiry
	a.mu.Unlock()
	return tok, nil
}

// ghAppExchanger is the real appExchanger: it builds a JWT-authed go-github client
// per call (the App JWT is short-lived, so we don't cache the client) and calls
// Apps.CreateInstallationToken.
type ghAppExchanger struct{}

// NewAppExchanger returns the production appExchanger backed by go-github.
func NewAppExchanger() appExchanger { return ghAppExchanger{} }

func (ghAppExchanger) CreateInstallationToken(ctx stdctx.Context, appJWT string, installID int64) (string, time.Time, error) {
	c := gh.NewClient(&http.Client{Timeout: 30 * time.Second}).WithAuthToken(appJWT)
	t, _, err := c.Apps.CreateInstallationToken(ctx, installID, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	return t.GetToken(), t.GetExpiresAt().Time, nil
}
