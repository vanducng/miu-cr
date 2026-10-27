package wire

import (
	stdctx "context"
	"log/slog"
	"sync"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/store/postgres"
)

var pgOAuthStores = struct {
	sync.Mutex
	byConn map[string]*postgres.OAuthStore
}{byConn: map[string]*postgres.OAuthStore{}}

func openOAuthStore(ctx stdctx.Context) (config.OAuthStore, error) {
	// Load returns defaults on a broken config.toml, which must not block login or init (it rewrites it).
	cfg, lerr := config.Load()
	if lerr != nil {
		slog.Warn("oauth store: config load failed, selecting the backend from MIUCR_STORE_BACKEND or the file default",
			"reason_code", "oauth.store_config_fallback", "reason", config.RedactString(lerr.Error()))
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
	return cachedPGOAuthStore(ctx, dsn, key)
}

// Process-lifetime pool: a resolved credential's Refresh closure keeps using the store after resolve returns.
func cachedPGOAuthStore(ctx stdctx.Context, dsn string, key []byte) (*postgres.OAuthStore, error) {
	conn := dsn + "\x00" + string(key)
	pgOAuthStores.Lock()
	defer pgOAuthStores.Unlock()
	if st, ok := pgOAuthStores.byConn[conn]; ok {
		return st, nil
	}
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	st, err := s.OAuthStore(key)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	pgOAuthStores.byConn[conn] = st
	return st, nil
}
