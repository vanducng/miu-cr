package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func TestReviewCallTimeoutValidation(t *testing.T) {
	for _, tt := range []struct {
		value   string
		wantErr bool
	}{
		{"", false},
		{"0s", false},
		{"25m", false},
		{"90s", false},
		{"-1s", true},
		{"soon", true},
		{"10", true},
	} {
		err := ValidateReview(Review{CallTimeout: tt.value})
		if (err != nil) != tt.wantErr {
			t.Fatalf("call_timeout %q: err=%v wantErr=%v", tt.value, err, tt.wantErr)
		}
		if err != nil {
			var ce *clierr.CLIError
			if !errors.As(err, &ce) || ce.Code != "config.invalid" || !strings.Contains(ce.Message, "call_timeout") {
				t.Fatalf("call_timeout %q: want config.invalid naming call_timeout, got %v", tt.value, err)
			}
		}
	}
}

func TestMergeReviewCarriesCallTimeout(t *testing.T) {
	if got := mergeReview(Review{CallTimeout: "10m"}, Review{}).CallTimeout; got != "10m" {
		t.Fatalf("an unset override must keep the base call_timeout, got %q", got)
	}
	if got := mergeReview(Review{CallTimeout: "10m"}, Review{CallTimeout: "25m"}).CallTimeout; got != "25m" {
		t.Fatalf("the override call_timeout must win, got %q", got)
	}
}

func TestLoadHostRejectsBadCallTimeout(t *testing.T) {
	for name, body := range map[string]string{
		"host.review": minimalHostYAML() + `
  review:
    call_timeout: soon
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`,
		"repos[0].review": minimalHostYAML() + `
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    review:
      call_timeout: -5m
`,
	} {
		err := loadHostErr(writeHostConfig(t, body))
		if !isConfigInvalid(err) || !strings.Contains(err.Error(), "call_timeout") {
			t.Fatalf("%s: want config.invalid naming call_timeout, got %v", name, err)
		}
	}
}

func TestLoadHostAcceptsCallTimeoutPerRepo(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
  review:
    call_timeout: 12m
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    review:
      call_timeout: 25m
`)
	cfg, err := LoadHost(path)
	if err != nil {
		t.Fatalf("LoadHost: %v", err)
	}
	if cfg.Host.Review.CallTimeout != "12m" || cfg.Repos[0].Review.CallTimeout != "25m" {
		t.Fatalf("call_timeout not loaded: host=%q repo=%q", cfg.Host.Review.CallTimeout, cfg.Repos[0].Review.CallTimeout)
	}
}
