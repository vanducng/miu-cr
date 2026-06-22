package wire

import (
	stdctx "context"
	"fmt"
	"log/slog"
	"os"

	"github.com/vanducng/miu-cr/internal/cli"
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

// requirePGDSN fast-fails when backend=postgres but no DSN is set: sql.Open("pgx","")
// is lazy and would otherwise hang ~10s to a cryptic store.unavailable on Ping.
func requirePGDSN(cfg config.Config) (string, error) {
	dsn := pgDSN(cfg)
	if dsn == "" {
		return "", &cli.CLIError{
			Code:    "config.invalid",
			Message: "store backend is postgres but no DSN is configured",
			Hint:    "set MIUCR_PG_DSN or [store] dsn",
			Exit:    2,
		}
	}
	return dsn, nil
}

// validateBackend resolves the backend and rejects an unrecognized explicit value
// (e.g. a "postgre" typo) with a typed, redacted config.invalid CLIError instead
// of silently falling back to sqlite. Empty/unset resolves to sqlite (the default).
func validateBackend(cfg config.Config) (string, error) {
	backend := resolveBackend(cfg)
	switch backend {
	case "sqlite", "postgres":
		return backend, nil
	default:
		return "", &cli.CLIError{
			Code:    "config.invalid",
			Message: config.RedactString(fmt.Sprintf("unknown store backend %q (want \"sqlite\" or \"postgres\")", backend)),
			Hint:    "set [store] backend to sqlite or postgres",
			Exit:    2,
		}
	}
}

// openStore opens the review store for the selected backend, returning the store,
// a closer, and an error. An unknown backend surfaces config.invalid; a postgres
// open failure surfaces a typed redacted store.unavailable CLIError; never a
// panic, never a silent nil.
func openStore(ctx stdctx.Context, cfg config.Config) (store.Store, func(), error) {
	backend, err := validateBackend(cfg)
	if err != nil {
		return nil, nil, err
	}
	switch backend {
	case "postgres":
		dsn, err := requirePGDSN(cfg)
		if err != nil {
			return nil, nil, err
		}
		s, err := postgres.Open(ctx, dsn)
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
	backend, err := validateBackend(cfg)
	if err != nil {
		return nil, nil, err
	}
	switch backend {
	case "postgres":
		dsn, err := requirePGDSN(cfg)
		if err != nil {
			return nil, nil, err
		}
		s, err := postgres.Open(ctx, dsn)
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
		// Unreachable: openStore only yields *postgres.Store or *sqlite.Store after
		// validateBackend. Panic rather than return nil so a future backend that
		// forgets its adapter fails here, not on a later nil-deref in the engine.
		panic(fmt.Sprintf("engineStoreFor: unsupported concrete store type %T", s))
	}
}

// newHistoryStore is the review-path history-store seam (overridable in tests to
// inject a temp store). It returns nil (no save) when history is disabled by
// config or --no-save. A store-open failure MUST degrade to no-save + a warning,
// never fail the review.
var newHistoryStore = func(ctx stdctx.Context, cfg config.Config, noSave bool) (store.Store, func(), error) {
	if noSave || !cfg.History.On() {
		return nil, nil, nil
	}
	return openStore(ctx, cfg)
}

// openHistoryStore opens the history store for the review path, degrading to
// (nil, nil) with a logged, redacted warning on any open failure so the review
// still emits findings.
func openHistoryStore(ctx stdctx.Context, cfg config.Config, noSave bool) (store.Store, func()) {
	s, closeStore, err := newHistoryStore(ctx, cfg, noSave)
	if err != nil {
		slog.Warn("history store disabled, review not saved: " + config.RedactString(err.Error()))
		return nil, nil
	}
	return s, closeStore
}

// pruneHistory trims the history store to cfg.History.MaxRecords (oldest dropped)
// after a save. Best-effort: a nil store, no cap, or a prune error is logged and
// never affects the review outcome.
func pruneHistory(ctx stdctx.Context, s store.Store, cfg config.Config) {
	if s == nil || cfg.History.MaxRecords <= 0 {
		return
	}
	if _, err := s.PruneReviews(ctx, store.PrunePolicy{Keep: cfg.History.MaxRecords}); err != nil {
		slog.Warn("history prune failed: " + config.RedactString(err.Error()))
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
