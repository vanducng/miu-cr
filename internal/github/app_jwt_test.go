package github

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestMintAppJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	tok, err := mintAppJWT(key, "12345", now)
	if err != nil {
		t.Fatalf("mintAppJWT: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}

	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("verify sig: %v", err)
	}

	var header map[string]string
	decodeSegment(t, parts[0], &header)
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("unexpected header: %v", header)
	}

	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	decodeSegment(t, parts[1], &claims)

	if want := now.Add(-60 * time.Second).Unix(); claims.Iat != want {
		t.Fatalf("iat = %d, want %d (now-60s)", claims.Iat, want)
	}
	if span := claims.Exp - claims.Iat; span > int64((10 * time.Minute).Seconds()) {
		t.Fatalf("exp-iat = %ds, want <= 600s", span)
	}
	if claims.Exp <= now.Unix() {
		t.Fatalf("exp = %d, want > now %d", claims.Exp, now.Unix())
	}
	if claims.Iss != "12345" {
		t.Fatalf("iss = %q, want 12345", claims.Iss)
	}
}

func TestMintAppJWTNilKey(t *testing.T) {
	if _, err := mintAppJWT(nil, "1", time.Now()); err == nil {
		t.Fatal("want error for nil key")
	}
}

func TestParsePrivateKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	pkcs1 := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})

	tests := []struct {
		name    string
		pem     []byte
		wantErr bool
	}{
		{"pkcs1", pkcs1, false},
		{"pkcs8", pkcs8, false},
		{"garbage", []byte("not a pem"), true},
		{"empty", nil, true},
		{"non-rsa-pkcs8", nonRSAPKCS8(t), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePrivateKey(tt.pem)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePrivateKey: %v", err)
			}
			if got.N.Cmp(key.N) != 0 {
				t.Fatal("parsed key modulus mismatch")
			}
		})
	}
}

func nonRSAPKCS8(t *testing.T) []byte {
	t.Helper()
	// A PKCS#8 block wrapping a non-RSA (Ed25519) key.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	b, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b})
}

func decodeSegment(t *testing.T, seg string, v any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal segment: %v", err)
	}
}
