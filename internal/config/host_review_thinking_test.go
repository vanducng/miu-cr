package config

import "testing"

func TestValidateHostReviewThinking(t *testing.T) {
	for _, v := range []string{"", "auto", "off", "low", "medium", "high"} {
		if err := validateHostReview("cfg", "review", HostReview{Thinking: v}); err != nil {
			t.Fatalf("thinking=%q should be valid: %v", v, err)
		}
	}
	if err := validateHostReview("cfg", "review", HostReview{Thinking: "ultra"}); err == nil {
		t.Fatal("thinking=ultra must be rejected")
	}
}
