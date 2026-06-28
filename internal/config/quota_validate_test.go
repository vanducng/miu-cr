package config

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func TestValidateProviderQuotas(t *testing.T) {
	tests := []struct {
		name    string
		quota   *QuotaConfig
		wantErr bool
	}{
		{"nil quota = no quota", nil, false},
		{"valid tokens 5h", &QuotaConfig{Dimension: "tokens", Limit: 2_000_000, Window: "5h"}, false},
		{"valid requests monthly", &QuotaConfig{Dimension: "requests", Limit: 100, Window: "monthly"}, false},
		{"empty dimension defaults tokens", &QuotaConfig{Limit: 1000, Window: "24h"}, false},
		{"valid 1h window", &QuotaConfig{Limit: 1000, Window: "1h"}, false},
		{"bad dimension cost (not yet supported)", &QuotaConfig{Dimension: "cost", Limit: 25, Window: "monthly"}, true},
		{"bad dimension typo", &QuotaConfig{Dimension: "tokenz", Limit: 1000, Window: "5h"}, true},
		{"zero limit", &QuotaConfig{Dimension: "tokens", Limit: 0, Window: "5h"}, true},
		{"negative limit", &QuotaConfig{Dimension: "tokens", Limit: -5, Window: "5h"}, true},
		{"empty window (no reset period)", &QuotaConfig{Dimension: "tokens", Limit: 1000}, true},
		{"unparsable window", &QuotaConfig{Dimension: "tokens", Limit: 1000, Window: "5x"}, true},
		{"zero-duration window", &QuotaConfig{Dimension: "tokens", Limit: 1000, Window: "0s"}, true},
		{"sub-second window rejected", &QuotaConfig{Dimension: "tokens", Limit: 1000, Window: "500ms"}, true},
		{"one-second window ok", &QuotaConfig{Dimension: "tokens", Limit: 1000, Window: "1s"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProviderQuotas(map[string]Provider{"p": {Quota: tt.quota}})
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateProviderQuotas err=%v wantErr=%v", err, tt.wantErr)
			}
			if err != nil {
				var ce *clierr.CLIError
				if !errors.As(err, &ce) || ce.Code != "config.invalid" {
					t.Fatalf("want typed config.invalid, got %v", err)
				}
			}
		})
	}
}

func TestQuotaDimensionDefault(t *testing.T) {
	if got := (QuotaConfig{}).QuotaDimension(); got != "tokens" {
		t.Fatalf("empty dimension should default to tokens, got %q", got)
	}
	if got := (QuotaConfig{Dimension: "requests"}).QuotaDimension(); got != "requests" {
		t.Fatalf("explicit dimension should pass through, got %q", got)
	}
}
