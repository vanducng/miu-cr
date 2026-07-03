package config

import "testing"

func TestValidateHostReviewDebounce(t *testing.T) {
	for _, v := range []string{"", "0s", "90s", "2m", "10m"} {
		if err := validateHostReview("cfg", "review", HostReview{Debounce: v}); err != nil {
			t.Fatalf("debounce=%q should be valid: %v", v, err)
		}
	}
	for _, v := range []string{"-5s", "soon"} {
		if err := validateHostReview("cfg", "review", HostReview{Debounce: v}); err == nil {
			t.Fatalf("debounce=%q must be rejected", v)
		}
	}
}
