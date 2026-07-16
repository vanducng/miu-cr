package wire

import (
	"testing"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/config"
)

// A request-level anchor-recovery override (set per-repo by the serve host) must
// resolve to the effective engine value AND rotate the review-reuse fingerprint;
// a nil override inherits the machine config and leaves the key untouched.
func TestAnchorRecoveryOverrideEffectiveValueAndReuseKey(t *testing.T) {
	off, on := false, true

	cfg := config.Defaults()
	req := cli.PRReviewRequest{Post: true, Gate: "high"}
	base := reviewReuseKey(req, cfg)
	applyRequestConfigOverrides(&cfg, req)
	if !cfg.Review.AnchorRecoveryOn() {
		t.Fatal("nil override must inherit the machine config (default on)")
	}
	if got := reviewReuseKey(req, cfg); got != base {
		t.Fatal("nil override must not change the reuse key")
	}

	cfg = config.Defaults()
	req.AnchorRecovery = &off
	applyRequestConfigOverrides(&cfg, req)
	if cfg.Review.AnchorRecoveryOn() {
		t.Fatal("host override off must win over the machine default")
	}
	if got := reviewReuseKey(req, cfg); got == base {
		t.Fatal("effective anchor_recovery flip via host override must change the reuse key")
	}

	cfg = config.Defaults()
	cfg.Review.AnchorRecovery = &off
	machineOff := reviewReuseKey(cli.PRReviewRequest{Post: true, Gate: "high"}, cfg)
	req.AnchorRecovery = &on
	applyRequestConfigOverrides(&cfg, req)
	if !cfg.Review.AnchorRecoveryOn() {
		t.Fatal("host override on must win over machine config off")
	}
	if got := reviewReuseKey(req, cfg); got == machineOff {
		t.Fatal("host override flipping anchor_recovery back on must change the reuse key")
	}
}
