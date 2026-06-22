package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// parsePrivateKey parses a GitHub App private key from a PEM block, accepting
// both PKCS#1 ("RSA PRIVATE KEY") and PKCS#8 ("PRIVATE KEY") encodings. The
// caller is responsible for zeroing the raw PEM bytes after parse; only RSA
// keys are accepted (GitHub App keys are RSA).
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("github: no PEM block found in private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github: private key is not RSA (got %T)", parsed)
	}
	return key, nil
}

// mintAppJWT signs a GitHub App authentication JWT (RS256) for appID. The iat is
// back-dated ~60s to absorb clock skew and exp is now+9m (GitHub rejects >10min).
func mintAppJWT(key *rsa.PrivateKey, appID string, now time.Time) (string, error) {
	if key == nil {
		return "", errors.New("github: nil private key")
	}
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	}

	headerSeg, err := encodeJSONSegment(header)
	if err != nil {
		return "", err
	}
	claimsSeg, err := encodeJSONSegment(claims)
	if err != nil {
		return "", err
	}

	signingInput := headerSeg + "." + claimsSeg
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github: sign app jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func encodeJSONSegment(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("github: marshal jwt segment: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
