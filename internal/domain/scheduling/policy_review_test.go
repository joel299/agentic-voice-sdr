package scheduling

import (
	"errors"
	"testing"
	"time"
)

func TestNewRetryPolicyRejectsOutOfRangeWeekdays(t *testing.T) {
	tests := []struct {
		name    string
		weekday time.Weekday
	}{
		{name: "weekday above valid range", weekday: time.Weekday(7)},
		{name: "weekday below valid range", weekday: time.Weekday(-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRetryPolicy(RetryPolicyConfig{
				MaxAttempts: 3,
				RetryAfter:  time.Hour,
				TimeZone:    "America/Sao_Paulo",
				Windows: map[time.Weekday][]BusinessWindow{
					tt.weekday: {{Open: 9 * time.Hour, Close: 17 * time.Hour}},
				},
			})
			var target *InvalidBusinessWindowError
			if !errors.As(err, &target) {
				t.Fatalf("NewRetryPolicy() error = %v, want *InvalidBusinessWindowError", err)
			}
		})
	}
}

func TestRetryPolicyDSTUsesLocalWallClockBoundaries(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewRetryPolicy(RetryPolicyConfig{
		MaxAttempts: 3,
		RetryAfter:  time.Hour,
		TimeZone:    "America/New_York",
		Windows: map[time.Weekday][]BusinessWindow{
			time.Sunday: {{Open: 9 * time.Hour, Close: 17 * time.Hour}},
		},
		Clock: func() time.Time {
			return time.Date(2026, time.March, 8, 8, 30, 0, 0, location)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, time.March, 8, 9, 30, 0, 0, location)
	got, err := policy.NextRetry(1)
	if err != nil {
		t.Fatalf("NextRetry() error = %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("NextRetry() = %s, want %s", got, want)
	}
	if err := policy.ValidateCallback(want); err != nil {
		t.Fatalf("ValidateCallback(%s) error = %v", want, err)
	}
}
