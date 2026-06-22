package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// OAuthRecord is the cached credential `miucr login` writes and review-time
// reads. It lives ONLY in oauth.json (0600); tokens never appear in the
// envelope, logs, or any error string.
type OAuthRecord struct {
	Provider     string    `json:"provider"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	APIKey       string    `json:"api_key,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Expired reports whether the access token is at or past its expiry as of now.
// A zero ExpiresAt is treated as never-expiring.
func (r OAuthRecord) Expired(now time.Time) bool {
	if r.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(r.ExpiresAt)
}

// ExpiringWithin reports whether the access token expires within d of now (used
// to refresh proactively). A zero ExpiresAt is treated as never-expiring.
func (r OAuthRecord) ExpiringWithin(d time.Duration, now time.Time) bool {
	if r.ExpiresAt.IsZero() {
		return false
	}
	return now.Add(d).After(r.ExpiresAt)
}

// OAuthPath returns the cached-token file location (Dir()/oauth.json).
func OAuthPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "oauth.json"), nil
}

// SaveOAuth writes rec to OAuthPath() atomically with safe perms (dir 0700,
// file 0600), copied from the config Save pattern.
func SaveOAuth(rec OAuthRecord) error {
	path, err := OAuthPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	out, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode oauth: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "oauth-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp oauth: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp oauth: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp oauth: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp oauth: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install oauth %s: %w", path, err)
	}
	return nil
}

// LoadOAuth reads the cached record. A missing file yields (zero, false, nil);
// any other read/parse error is returned with ok=false.
func LoadOAuth() (OAuthRecord, bool, error) {
	path, err := OAuthPath()
	if err != nil {
		return OAuthRecord{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return OAuthRecord{}, false, nil
	}
	if err != nil {
		return OAuthRecord{}, false, fmt.Errorf("read oauth %s: %w", path, err)
	}
	var rec OAuthRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return OAuthRecord{}, false, fmt.Errorf("parse oauth %s: %w", path, err)
	}
	return rec, true, nil
}
