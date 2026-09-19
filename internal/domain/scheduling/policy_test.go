package scheduling

import (
	"errors"
	"testing"
	"time"
)

func testWindows() map[time.Weekday][]BusinessWindow {
	return map[time.Weekday][]BusinessWindow{
		time.Monday:    {{Open: 9 * time.Hour, Close: 17 * time.Hour}},
		time.Wednesday: {{Open: 9 * time.Hour, Close: 17 * time.Hour}},
		time.Friday:    {{Open: 9 * time.Hour, Close: 17 * time.Hour}},
	}
}

func fixedPolicy(t *testing.T, now time.Time, windows map[time.Weekday][]BusinessWindow) *RetryPolicy {
	t.Helper()
	policy, err := NewRetryPolicy(RetryPolicyConfig{
		MaxAttempts: 3,
		RetryAfter:  time.Hour,
		TimeZone:    "America/Sao_Paulo",
		Windows:     windows,
		Clock:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewRetryPolicy() error = %v", err)
	}
	return policy
}

func TestRetryPolicyCanRetryTable(t *testing.T) {
	policy := fixedPolicy(t, time.Date(2026, time.March, 16, 10, 0, 0, 0, time.UTC), testWindows())

	tests := []struct {
		name    string
		attempt int
		want    bool
	}{
		{name: "first attempt", attempt: 1, want: true},
		{name: "second attempt", attempt: 2, want: true},
		{name: "third attempt reaches limit", attempt: 3, want: false},
		{name: "fourth attempt remains rejected", attempt: 4, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.CanRetry(tt.attempt); got != tt.want {
				t.Fatalf("CanRetry(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestRetryPolicyNextRetryTable(t *testing.T) {
	location, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		now         time.Time
		want        time.Time
		attempt     int
		wantExhaust bool
	}{
		{name: "one hour inside window", now: time.Date(2026, time.March, 16, 10, 0, 0, 0, location), want: time.Date(2026, time.March, 16, 11, 0, 0, 0, location)},
		{name: "before opening moves to opening", now: time.Date(2026, time.March, 16, 7, 0, 0, 0, location), want: time.Date(2026, time.March, 16, 9, 0, 0, 0, location)},
		{name: "after closing moves to next configured day", now: time.Date(2026, time.March, 16, 16, 30, 0, 0, location), want: time.Date(2026, time.March, 18, 9, 0, 0, 0, location)},
		{name: "crosses configured day boundary", now: time.Date(2026, time.March, 17, 16, 30, 0, 0, location), want: time.Date(2026, time.March, 18, 9, 0, 0, 0, location)},
		{name: "third attempt is exhausted", now: time.Date(2026, time.March, 16, 10, 0, 0, 0, location), attempt: 3, wantExhaust: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := fixedPolicy(t, tt.now, testWindows())
			attempt := tt.attempt
			if attempt == 0 {
				attempt = 1
			}
			got, err := policy.NextRetry(attempt)
			if tt.wantExhaust {
				var target *AttemptsExhaustedError
				if !errors.As(err, &target) {
					t.Fatalf("NextRetry() error = %v, want *AttemptsExhaustedError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NextRetry() error = %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("NextRetry() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRetryPolicyUsesConfiguredTimezoneAndClock(t *testing.T) {
	location, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.March, 16, 13, 0, 0, 0, time.FixedZone("UTC-3", -3*60*60))
	policy := fixedPolicy(t, now, testWindows())

	got, err := policy.NextRetry(1)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.March, 16, 14, 0, 0, 0, location)
	if !got.Equal(want) || got.Location().String() != location.String() {
		t.Fatalf("NextRetry() = %s (%s), want %s (%s)", got, got.Location(), want, want.Location())
	}
}

func TestRetryPolicyValidateCallbackTable(t *testing.T) {
	location, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	policy := fixedPolicy(t, time.Date(2026, time.March, 16, 10, 0, 0, 0, location), testWindows())

	tests := []struct {
		name      string
		requested time.Time
		wantError bool
	}{
		{name: "valid callback", requested: time.Date(2026, time.March, 16, 10, 0, 0, 0, location)},
		{name: "callback before opening", requested: time.Date(2026, time.March, 16, 8, 59, 0, 0, location), wantError: true},
		{name: "callback after closing", requested: time.Date(2026, time.March, 16, 17, 1, 0, 0, location), wantError: true},
		{name: "callback on unconfigured day", requested: time.Date(2026, time.March, 17, 10, 0, 0, 0, location), wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := policy.ValidateCallback(tt.requested)
			var callbackErr *CallbackOutsidePolicyError
			if tt.wantError {
				if !errors.As(err, &callbackErr) {
					t.Fatalf("ValidateCallback() error = %v, want *CallbackOutsidePolicyError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateCallback() error = %v", err)
			}
		})
	}
}

func TestNewRetryPolicyRejectsInvalidConfigurationTable(t *testing.T) {
	tests := []struct {
		name         string
		config       RetryPolicyConfig
		wantWindow   bool
		wantTimezone bool
	}{
		{name: "invalid business window", config: RetryPolicyConfig{MaxAttempts: 3, RetryAfter: time.Hour, TimeZone: "America/Sao_Paulo", Windows: map[time.Weekday][]BusinessWindow{time.Monday: {{Open: 17 * time.Hour, Close: 9 * time.Hour}}}}, wantWindow: true},
		{name: "invalid timezone", config: RetryPolicyConfig{MaxAttempts: 3, RetryAfter: time.Hour, TimeZone: "Not/AZone", Windows: testWindows()}, wantTimezone: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRetryPolicy(tt.config)
			var windowErr *InvalidBusinessWindowError
			var timezoneErr *InvalidTimezoneError
			switch {
			case tt.wantWindow && !errors.As(err, &windowErr):
				t.Fatalf("NewRetryPolicy() error = %v, want *InvalidBusinessWindowError", err)
			case tt.wantTimezone && !errors.As(err, &timezoneErr):
				t.Fatalf("NewRetryPolicy() error = %v, want *InvalidTimezoneError", err)
			}
		})
	}
}

func TestNewRetryPolicyRejectsAttemptsAboveProductMaximum(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
		wantErr     bool
	}{
		{name: "one attempt", maxAttempts: 1},
		{name: "two attempts", maxAttempts: 2},
		{name: "three attempts", maxAttempts: 3},
		{name: "four attempts", maxAttempts: 4, wantErr: true},
		{name: "ten attempts", maxAttempts: 10, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRetryPolicy(RetryPolicyConfig{
				MaxAttempts: tt.maxAttempts,
				RetryAfter:  time.Hour,
				TimeZone:    "America/Sao_Paulo",
				Windows:     testWindows(),
			})
			var configErr *InvalidBusinessWindowError
			if tt.wantErr {
				if !errors.As(err, &configErr) {
					t.Fatalf("NewRetryPolicy() error = %v, want *InvalidBusinessWindowError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRetryPolicy() error = %v", err)
			}
		})
	}
}

func TestNewRetryPolicyRejectsRetryIntervalsOtherThanOneHour(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter time.Duration
		wantErr    bool
	}{
		{name: "one hour", retryAfter: time.Hour},
		{name: "thirty minutes", retryAfter: 30 * time.Minute, wantErr: true},
		{name: "two hours", retryAfter: 2 * time.Hour, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRetryPolicy(RetryPolicyConfig{
				MaxAttempts: 3,
				RetryAfter:  tt.retryAfter,
				TimeZone:    "America/Sao_Paulo",
				Windows:     testWindows(),
			})
			var configErr *InvalidBusinessWindowError
			if tt.wantErr {
				if !errors.As(err, &configErr) {
					t.Fatalf("NewRetryPolicy() error = %v, want *InvalidBusinessWindowError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRetryPolicy() error = %v", err)
			}
		})
	}
}
