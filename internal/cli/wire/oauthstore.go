package wire

import (
	stdctx "context"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/store/postgres"
)

const oauthStoreOpenTimeout = 30 * time.Second

var pgOAuthStores = struct {
	sync.Mutex
	byConn map[string]*postgres.OAuthStore
	opens  singleflight.Group
}{byConn: map[string]*postgres.OAuthStore{}}

func openOAuthStore(ctx stdctx.Context) (config.OAuthStore, error) {
	cfg, lerr := config.Load()
	if lerr != nil {
		// A broken config.toml may hide backend = "postgres", so only the env var can still decide.
		if os.Getenv("MIUCR_STORE_BACKEND") == "" {
			return nil, lerr
		}
		slog.Warn("oauth store: config load failed, using MIUCR_STORE_BACKEND",
			"reason_code", "oauth.store_config_env_only", "reason", config.RedactString(lerr.Error()))
	}
	backend, err := validateBackend(cfg)
	if err != nil {
		return nil, err
	}
	if backend != "postgres" {
		return config.FileOAuthStore{}, nil
	}
	dsn, err := requirePGDSN(cfg)
	if err != nil {
		return nil, err
	}
	key, err := config.OAuthKeyFromEnv()
	if err != nil {
		return nil, err
	}
	st, err := cachedPGOAuthStore(ctx, dsn, key)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// Process-lifetime pool: a resolved credential's Refresh closure keeps using the store after resolve returns.
func cachedPGOAuthStore(ctx stdctx.Context, dsn string, key []byte) (*postgres.OAuthStore, error) {
	conn := dsn + "\x00" + string(key)
	if st := cachedOAuthStore(conn); st != nil {
		return st, nil
	}
	v, err, _ := pgOAuthStores.opens.Do(conn, func() (any, error) {
		if st := cachedOAuthStore(conn); st != nil {
			return st, nil
		}
		// Detached so one caller's cancel cannot fail every collapsed waiter.
		octx, cancel := stdctx.WithTimeout(stdctx.WithoutCancel(ctx), oauthStoreOpenTimeout)
		defer cancel()
		s, err := postgres.Open(octx, dsn)
		if err != nil {
			return nil, err
		}
		st, err := s.OAuthStore(key)
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		pgOAuthStores.Lock()
		pgOAuthStores.byConn[conn] = st
		pgOAuthStores.Unlock()
		return st, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*postgres.OAuthStore), nil
}

func cachedOAuthStore(conn string) *postgres.OAuthStore {
	pgOAuthStores.Lock()
	defer pgOAuthStores.Unlock()
	return pgOAuthStores.byConn[conn]
}
