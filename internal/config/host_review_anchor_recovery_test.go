package config

import "testing"

func TestValidateHostReviewAnchorRecovery(t *testing.T) {
	off := false
	on := true
	for _, v := range []*bool{nil, &off, &on} {
		if err := validateHostReview("cfg", "review", HostReview{AnchorRecovery: v}); err != nil {
			t.Fatalf("anchor_recovery=%v should be valid: %v", v, err)
		}
	}
}

func TestLoadHostAnchorRecoveryAtEveryLayer(t *testing.T) {
	path := writeHostConfig(t, `version: 1
store:
  backend: postgres
github:
  default_account: pat
  accounts:
    pat:
      mode: pat
      auth_env: GITHUB_TOKEN
review:
  anchor_recovery: false
host:
  review:
    anchor_recovery: true
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
  - name: data-platform
    slug: example-org/data-platform
    git_url: https://github.com/example-org/data-platform.git
    review:
      anchor_recovery: false
`)
	cfg, err := LoadHost(path)
	if err != nil {
		t.Fatalf("LoadHost: %v", err)
	}
	if cfg.Review.AnchorRecovery == nil || *cfg.Review.AnchorRecovery {
		t.Fatalf("review.anchor_recovery = %v, want explicit false", cfg.Review.AnchorRecovery)
	}
	if cfg.Host.Review.AnchorRecovery == nil || !*cfg.Host.Review.AnchorRecovery {
		t.Fatalf("host.review.anchor_recovery = %v, want explicit true", cfg.Host.Review.AnchorRecovery)
	}
	if cfg.Repos[0].Review.AnchorRecovery != nil {
		t.Fatalf("repos[0].review.anchor_recovery = %v, want nil (inherit)", cfg.Repos[0].Review.AnchorRecovery)
	}
	if cfg.Repos[1].Review.AnchorRecovery == nil || *cfg.Repos[1].Review.AnchorRecovery {
		t.Fatalf("repos[1].review.anchor_recovery = %v, want explicit false", cfg.Repos[1].Review.AnchorRecovery)
	}
}
