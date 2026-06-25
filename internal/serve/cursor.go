package serve

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// cursorFile is the poll-cursor filename under config.Dir().
const cursorFile = "poll-cursor.json"

// pruneAge drops Seen/NotifSeen entries not touched within this window. Pruning
// is by staleness (an entry untouched for a fortnight), NOT absence-from-latest:
// an open PR no longer in a tick's candidate set must keep its seen-head so it
// is never re-reviewed.
const pruneAge = 14 * 24 * time.Hour

// Cursor is the restart-safe poll dedup state. Seen maps a PR ref
// (owner/repo#N) to the head SHA last reviewed successfully; NotifSeen maps a
// ref to the notification updated_at last observed (the pre-GetPR cost guard).
// The GitHub token is NEVER a field here, it is resolved per-tick in memory.
type Cursor struct {
	Since     time.Time            `json:"since"`
	Seen      map[string]string    `json:"seen"`
	NotifSeen map[string]string    `json:"notif_seen"`
	touched   map[string]time.Time `json:"-"`
}

func newCursor() *Cursor {
	return &Cursor{
		Seen:      map[string]string{},
		NotifSeen: map[string]string{},
		touched:   map[string]time.Time{},
	}
}

// recordSeen records ref's reviewed head SHA (called on review success only).
func (c *Cursor) recordSeen(ref, sha string) {
	c.Seen[ref] = sha
	c.touched[ref] = time.Now()
}

// recordNotif records ref's last-observed notification updated_at.
func (c *Cursor) recordNotif(ref, updatedAt string) {
	c.NotifSeen[ref] = updatedAt
	c.touched[ref] = time.Now()
}

// prune drops entries untouched within pruneAge. touched is in-memory only, so a
// freshly-loaded cursor treats every entry as touched at load until next access.
func (c *Cursor) prune(now time.Time) {
	for ref, t := range c.touched {
		if now.Sub(t) <= pruneAge {
			continue
		}
		delete(c.Seen, ref)
		delete(c.NotifSeen, ref)
		delete(c.touched, ref)
	}
}

// cursorPath returns config.Dir()/poll-cursor.json.
func cursorPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cursorFile), nil
}

// loadCursor reads the cursor JSON. A missing or corrupt file is tolerated as an
// empty cursor (warn, never fatal) so a poller never refuses to start over a bad
// state file; load seeds touched=now so a stale on-disk entry survives one cycle.
func loadCursor(path string, log *slog.Logger) *Cursor {
	c := newCursor()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("poll cursor: read failed, starting empty", "error", err.Error())
		}
		return c
	}
	if err := json.Unmarshal(data, c); err != nil {
		log.Warn("poll cursor: corrupt file, starting empty", "error", err.Error())
		return newCursor()
	}
	if c.Seen == nil {
		c.Seen = map[string]string{}
	}
	if c.NotifSeen == nil {
		c.NotifSeen = map[string]string{}
	}
	// Deferred: touched is in-memory only, so the staleness clock resets each
	// restart, abandoned entries can outlive pruneAge across frequent restarts.
	// Acceptable: prune is best-effort dedup hygiene, not a correctness boundary.
	c.touched = map[string]time.Time{}
	now := time.Now()
	for ref := range c.Seen {
		c.touched[ref] = now
	}
	for ref := range c.NotifSeen {
		c.touched[ref] = now
	}
	return c
}

// save writes the cursor atomically: MkdirAll(0700) (config.Dir does not create
// the dir) + temp file in the same dir + rename. The token is never persisted.
func (c *Cursor) save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, cursorFile+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	// os.Rename is atomic-replace on all targets incl. Windows (Go maps it to
	// MoveFileEx with MOVEFILE_REPLACE_EXISTING), so overwriting the prior cursor
	// needs no pre-remove dance.
	return os.Rename(tmpName, path)
}
