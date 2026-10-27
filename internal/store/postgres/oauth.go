package postgres

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

const (
	oauthCredentialID            = "default"
	oauthSealVersion        byte = 1
	saveRefreshedBudget          = 20 * time.Second
	saveRefreshedMaxBackoff      = 2 * time.Second
)

var oauthSealAAD = []byte("miucr oauth_credentials " + oauthCredentialID)

// OAuthStore seals the login record (AES-256-GCM) so it survives restarts without a volume.
type OAuthStore struct {
	db     *sql.DB
	aead   cipher.AEAD
	macKey []byte
}

var _ config.OAuthStore = (*OAuthStore)(nil)

func (s *Store) OAuthStore(key []byte) (*OAuthStore, error) {
	return newOAuthStore(s.db, key)
}

func newOAuthStore(db *sql.DB, key []byte) (*OAuthStore, error) {
	if len(key) != config.OAuthKeySize {
		return nil, config.OAuthKeyError("oauth.key_invalid", fmt.Sprintf("%s must decode to %d bytes, got %d", config.OAuthKeyEnv, config.OAuthKeySize, len(key)))
	}
	sealKey, err := hkdf.Key(sha256.New, key, nil, "miucr oauth seal v1", 32)
	if err != nil {
		return nil, fmt.Errorf("derive oauth seal key: %w", err)
	}
	macKey, err := hkdf.Key(sha256.New, key, nil, "miucr oauth refresh fingerprint v1", 32)
	if err != nil {
		return nil, fmt.Errorf("derive oauth fingerprint key: %w", err)
	}
	block, err := aes.NewCipher(sealKey)
	if err != nil {
		return nil, fmt.Errorf("init oauth cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init oauth cipher: %w", err)
	}
	return &OAuthStore{db: db, aead: aead, macKey: macKey}, nil
}

func (o *OAuthStore) Backend() string { return config.OAuthStorePostgres }

func (o *OAuthStore) Load(ctx context.Context) (config.OAuthRecord, bool, error) {
	var sealed []byte
	err := o.db.QueryRowContext(ctx, `SELECT sealed FROM oauth_credentials WHERE id = $1`, oauthCredentialID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return config.OAuthRecord{}, false, nil
	}
	if err != nil {
		return config.OAuthRecord{}, false, unavailable("load oauth credential", err)
	}
	rec, err := o.open(sealed)
	if err != nil {
		return config.OAuthRecord{}, false, err
	}
	return rec, true, nil
}

func (o *OAuthStore) Save(ctx context.Context, rec config.OAuthRecord) error {
	sealed, err := o.seal(rec)
	if err != nil {
		return err
	}
	_, err = o.db.ExecContext(ctx,
		`INSERT INTO oauth_credentials (id, provider, account_id, expires_at, refresh_fp, sealed, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, now())
		 ON CONFLICT (id) DO UPDATE SET
		   provider = EXCLUDED.provider, account_id = EXCLUDED.account_id, expires_at = EXCLUDED.expires_at,
		   refresh_fp = EXCLUDED.refresh_fp, sealed = EXCLUDED.sealed, updated_at = now()`,
		oauthCredentialID, rec.Provider, rec.AccountID, nullTime(rec.ExpiresAt), o.fingerprint(rec.RefreshToken), sealed,
	)
	if err != nil {
		return unavailable("save oauth credential", err)
	}
	return nil
}

func (o *OAuthStore) SaveRefreshed(ctx context.Context, prior, next config.OAuthRecord) (bool, error) {
	sealed, err := o.seal(next)
	if err != nil {
		return false, err
	}
	// The provider already consumed prior's refresh token, so keep retrying through a DB blip.
	ctx, cancel := context.WithTimeout(ctx, saveRefreshedBudget)
	defer cancel()
	backoff := 200 * time.Millisecond
	for attempt := 1; ; attempt++ {
		// Matching next's fingerprint too keeps a retry after an unacknowledged commit idempotent.
		n, err := execRows(ctx, o.db,
			`UPDATE oauth_credentials SET
			   provider = $2, account_id = $3, expires_at = $4, refresh_fp = $5, sealed = $6, updated_at = now()
			 WHERE id = $1 AND refresh_fp IN ($5, $7)`,
			oauthCredentialID, next.Provider, next.AccountID, nullTime(next.ExpiresAt), o.fingerprint(next.RefreshToken), sealed, o.fingerprint(prior.RefreshToken),
		)
		if err == nil {
			return n > 0, nil
		}
		select {
		case <-ctx.Done():
			return false, unavailable(fmt.Sprintf("save refreshed oauth credential (%d attempts)", attempt), err)
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, saveRefreshedMaxBackoff)
	}
}

func (o *OAuthStore) Delete(ctx context.Context) (bool, error) {
	n, err := execRows(ctx, o.db, `DELETE FROM oauth_credentials WHERE id = $1`, oauthCredentialID)
	if err != nil {
		return false, unavailable("delete oauth credential", err)
	}
	return n > 0, nil
}

func (o *OAuthStore) seal(rec config.OAuthRecord) ([]byte, error) {
	plain, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("encode oauth credential: %w", err)
	}
	nonce := make([]byte, o.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("oauth seal nonce: %w", err)
	}
	out := append([]byte{oauthSealVersion}, nonce...)
	return o.aead.Seal(out, nonce, plain, oauthSealAAD), nil
}

func (o *OAuthStore) open(sealed []byte) (config.OAuthRecord, error) {
	n := o.aead.NonceSize()
	if len(sealed) < 1+n || sealed[0] != oauthSealVersion {
		return config.OAuthRecord{}, oauthDecryptFailed()
	}
	plain, err := o.aead.Open(nil, sealed[1:1+n], sealed[1+n:], oauthSealAAD)
	if err != nil {
		return config.OAuthRecord{}, oauthDecryptFailed()
	}
	var rec config.OAuthRecord
	if err := json.Unmarshal(plain, &rec); err != nil {
		return config.OAuthRecord{}, oauthDecryptFailed()
	}
	return rec, nil
}

func (o *OAuthStore) fingerprint(refreshToken string) []byte {
	m := hmac.New(sha256.New, o.macKey)
	m.Write([]byte(refreshToken))
	return m.Sum(nil)
}

func oauthDecryptFailed() error {
	return &clierr.CLIError{
		Code:    "oauth.decrypt_failed",
		Message: "stored oauth credential could not be decrypted with " + config.OAuthKeyEnv,
		Hint:    "set " + config.OAuthKeyEnv + " to the key used at login, or run `miucr login` again to replace the credential",
		Exit:    2,
	}
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
