package config

import "testing"

func TestValidateReviewTemperature(t *testing.T) {
	stubReviewValidators(t)
	mk := func(v float64) Review { return Review{Temperature: &v} }
	tests := []struct {
		name    string
		r       Review
		wantErr bool
	}{
		{"unset ok", Review{}, false},
		{"zero ok", mk(0), false},
		{"one ok", mk(1), false},
		{"two ok", mk(2), false},
		{"negative bad", mk(-0.1), true},
		{"too high bad", mk(2.5), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateReview(tt.r); (err != nil) != tt.wantErr {
				t.Fatalf("ValidateReview(%+v) err=%v wantErr=%v", tt.r, err, tt.wantErr)
			}
		})
	}
}
