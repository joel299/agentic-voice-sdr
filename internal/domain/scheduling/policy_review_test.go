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
