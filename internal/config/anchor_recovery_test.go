package config

import (
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// anchor_recovery defaults ON: an absent key (nil) resolves to true, both on the
// zero Review value and on the shipped Defaults().
func TestAnchorRecoveryDefaultOn(t *testing.T) {
	if !(Review{}).AnchorRecoveryOn() {
		t.Error("zero Review must resolve anchor_recovery to on")
	}
	if !Defaults().Review.AnchorRecoveryOn() {
		t.Error("Defaults() must resolve anchor_recovery to on")
	}
}

// [review].anchor_recovery = false parses from TOML and survives the layered
// merge (an explicit false beats the nil default).
func TestAnchorRecoveryExplicitFalseParsesAndMerges(t *testing.T) {
	var file Config
	if err := toml.Unmarshal([]byte("[review]\nanchor_recovery = false\n"), &file); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if file.Review.AnchorRecovery == nil || *file.Review.AnchorRecovery {
		t.Fatalf("parsed AnchorRecovery = %v, want explicit false", file.Review.AnchorRecovery)
	}
	merged := Merge(Defaults(), file)
	if merged.Review.AnchorRecoveryOn() {
		t.Error("explicit false must win over the default-on")
	}
}

// An explicit true overlays a base false (mirrors the Suggest/PatchRepair merge).
func TestAnchorRecoveryMergeTrueOverridesFalse(t *testing.T) {
	off, on := false, true
	base := Defaults()
	base.Review.AnchorRecovery = &off
	merged := Merge(base, Config{Review: Review{AnchorRecovery: &on}})
	if !merged.Review.AnchorRecoveryOn() {
		t.Error("explicit true must override base false")
	}
	// And a file that does not set it inherits base.
	merged = Merge(base, Config{})
	if merged.Review.AnchorRecoveryOn() {
		t.Error("unset file value must inherit base false")
	}
}

// reviewEqual must detect an anchor_recovery difference so `config set`/Save
// round-trips a user-set [review] table (mirrors the PatchRepair comparison).
func TestReviewEqualDetectsAnchorRecovery(t *testing.T) {
	off := false
	if reviewEqual(Review{}, Review{AnchorRecovery: &off}) {
		t.Error("reviewEqual must detect a set anchor_recovery")
	}
	on := true
	other := true
	if !reviewEqual(Review{AnchorRecovery: &on}, Review{AnchorRecovery: &other}) {
		t.Error("equal anchor_recovery values must compare equal")
	}
}
