package wire

import (
	stdctx "context"
	"os"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/postgres"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

// The backend factory lives in the wire layer (not package store): both sqlite
// and postgres import store, so a factory in store would cycle. It routes the
// existing sqlite open sites and adds the postgres path, selecting via env >
// config > "sqlite" default. The DSN prefers MIUCR_PG_DSN over cfg.Store.DSN and
// is always funneled through config.RedactString before any error escapes.

// resolveBackend picks the store backend: MIUCR_STORE_BACKEND env > [store]
// backend config > "sqlite". An empty config backend falls through to sqlite.
func resolveBackend(cfg config.Config) string {
	return firstNonEmpty(os.Getenv("MIUCR_STORE_BACKEND"), cfg.Store.Backend, "sqlite")
}

// pgDSN sources the postgres DSN: MIUCR_PG_DSN env preferred over cfg.Store.DSN.
func pgDSN(cfg config.Config) string {
	return firstNonEmpty(os.Getenv("MIUCR_PG_DSN"), cfg.Store.DSN)
}

// openStore opens the review store for the selected backend, returning the store,
// a closer, and an error. A postgres open failure surfaces a typed redacted
// store.unavailable CLIError; never a panic, never a silent nil.
func openStore(ctx stdctx.Context, cfg config.Config) (store.Store, func(), error) {
	switch resolveBackend(cfg) {
	case "postgres":
		s, err := postgres.Open(ctx, pgDSN(cfg))
		if err != nil {
			return nil, nil, err
		}
		return s, func() { _ = s.Close() }, nil
	default:
		path, err := sqlite.DefaultPath()
		if err != nil {
			return nil, nil, err
		}
		s, err := sqlite.Open(path)
		if err != nil {
			return nil, nil, err
		}
		return s, func() { _ = s.Close() }, nil
	}
}

// openPRThreadStore opens the PR-thread store for the selected backend. The
// returned store satisfies store.PRThreadStore; both backends' *Store do.
func openPRThreadStore(ctx stdctx.Context, cfg config.Config) (store.PRThreadStore, func(), error) {
	switch resolveBackend(cfg) {
	case "postgres":
		s, err := postgres.Open(ctx, pgDSN(cfg))
		if err != nil {
			return nil, nil, err
		}
		return s.PRThread(), func() { _ = s.Close() }, nil
	default:
		path, err := sqlite.DefaultPath()
		if err != nil {
			return nil, nil, err
		}
		s, err := sqlite.Open(path)
		if err != nil {
			return nil, nil, err
		}
		return s.PRThread(), func() { _ = s.Close() }, nil
	}
}

// engineStoreFor wraps a concrete backend store in its engine.Store adapter. The
// factory returns store.Store (the shared interface), but the engine persists via
// engine.PersistRecord, so the adapter must be selected by concrete type.
func engineStoreFor(s store.Store) engine.Store {
	switch v := s.(type) {
	case *postgres.Store:
		return postgres.EngineStore{S: v}
	case *sqlite.Store:
		return sqlite.EngineStore{S: v}
	default:
		return nil
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
