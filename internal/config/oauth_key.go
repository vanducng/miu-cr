package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

const (
	OAuthKeyEnv  = "MIUCR_OAUTH_KEY"
	OAuthKeySize = 32
)

const oauthKeyHint = "set " + OAuthKeyEnv + " to a base64 32-byte key (e.g. `openssl rand -base64 32`) in every process that shares the postgres store"

// OAuthKeyFromEnv returns the AES-256 key that seals tokens in the postgres oauth store.
func OAuthKeyFromEnv() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(OAuthKeyEnv))
	if raw == "" {
		return nil, OAuthKeyError("oauth.key_missing", OAuthKeyEnv+" is not set; the postgres oauth store refuses to store or read tokens without it")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, OAuthKeyError("oauth.key_invalid", OAuthKeyEnv+" is not valid base64")
	}
	if len(key) != OAuthKeySize {
		return nil, OAuthKeyError("oauth.key_invalid", fmt.Sprintf("%s must decode to %d bytes, got %d", OAuthKeyEnv, OAuthKeySize, len(key)))
	}
	return key, nil
}

func OAuthKeyError(code, msg string) error {
	return &clierr.CLIError{Code: code, Message: msg, Hint: oauthKeyHint, Exit: 2}
}
