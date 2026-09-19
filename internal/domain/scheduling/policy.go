package scheduling

import (
	"fmt"
	"sort"
	"time"
)

// BusinessWindow defines one inclusive-open, exclusive-close window in local time.
type BusinessWindow struct {
	Open  time.Duration
	Close time.Duration
}

// RetryPolicyConfig contains all inputs needed by RetryPolicy.
type RetryPolicyConfig struct {
	MaxAttempts int
	RetryAfter  time.Duration
	TimeZone    string
	Windows     map[time.Weekday][]BusinessWindow
	Clock       func() time.Time
}

// RetryPolicy is a deterministic retry and business-hours policy.
type RetryPolicy struct {
	maxAttempts int
	retryAfter  time.Duration
	location    *time.Location
	windows     map[time.Weekday][]BusinessWindow
	clock       func() time.Time
}

type AttemptsExhaustedError struct {
	Attempt     int
	MaxAttempts int
}

func (e *AttemptsExhaustedError) Error() string {
	return fmt.Sprintf("retry attempts exhausted: attempt=%d max=%d", e.Attempt, e.MaxAttempts)
}

type InvalidBusinessWindowError struct {
	Weekday time.Weekday
	Index   int
	Reason  string
}

func (e *InvalidBusinessWindowError) Error() string {
	return fmt.Sprintf("invalid business window for %s at index %d: %s", e.Weekday, e.Index, e.Reason)
}

type InvalidTimezoneError struct {
	Timezone string
	Err      error
}

func (e *InvalidTimezoneError) Error() string {
	return fmt.Sprintf("invalid timezone %q: %v", e.Timezone, e.Err)
}

func (e *InvalidTimezoneError) Unwrap() error { return e.Err }

type CallbackOutsidePolicyError struct {
	Requested time.Time
}

func (e *CallbackOutsidePolicyError) Error() string {
	return fmt.Sprintf("callback outside business-hours policy: %s", e.Requested)
}

func NewRetryPolicy(config RetryPolicyConfig) (*RetryPolicy, error) {
	if config.MaxAttempts <= 0 {
		return nil, &InvalidBusinessWindowError{Reason: "max attempts must be positive"}
	}
	if config.RetryAfter <= 0 {
		return nil, &InvalidBusinessWindowError{Reason: "retry interval must be positive"}
	}
	location, err := time.LoadLocation(config.TimeZone)
	if err != nil {
		return nil, &InvalidTimezoneError{Timezone: config.TimeZone, Err: err}
	}
	if len(config.Windows) == 0 {
		return nil, &InvalidBusinessWindowError{Reason: "at least one business window is required"}
	}

	windows := make(map[time.Weekday][]BusinessWindow, len(config.Windows))
	for weekday, entries := range config.Windows {
		if weekday < time.Sunday || weekday > time.Saturday {
			return nil, &InvalidBusinessWindowError{Weekday: weekday, Reason: "weekday must be between Sunday and Saturday"}
		}
		if len(entries) == 0 {
			return nil, &InvalidBusinessWindowError{Weekday: weekday, Reason: "day has no windows"}
		}
		copied := append([]BusinessWindow(nil), entries...)
		sort.Slice(copied, func(i, j int) bool { return copied[i].Open < copied[j].Open })
		for index, window := range copied {
			if window.Open < 0 || window.Close > 24*time.Hour || window.Open >= window.Close {
				return nil, &InvalidBusinessWindowError{Weekday: weekday, Index: index, Reason: "open and close must satisfy 0 <= open < close <= 24h"}
			}
			if index > 0 && window.Open < copied[index-1].Close {
				return nil, &InvalidBusinessWindowError{Weekday: weekday, Index: index, Reason: "windows overlap"}
			}
		}
		windows[weekday] = copied
	}

	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &RetryPolicy{maxAttempts: config.MaxAttempts, retryAfter: config.RetryAfter, location: location, windows: windows, clock: clock}, nil
}

func (p *RetryPolicy) CanRetry(attempt int) bool {
	return attempt >= 1 && attempt < p.maxAttempts
}

func (p *RetryPolicy) NextRetry(attempt int) (time.Time, error) {
	if !p.CanRetry(attempt) {
		return time.Time{}, &AttemptsExhaustedError{Attempt: attempt, MaxAttempts: p.maxAttempts}
	}
	return p.nextBusinessTime(p.clock().In(p.location).Add(p.retryAfter))
}

func (p *RetryPolicy) ValidateCallback(requested time.Time) error {
	local := requested.In(p.location)
	for _, window := range p.windows[local.Weekday()] {
		open := p.localBoundary(local, window.Open)
		close := p.localBoundary(local, window.Close)
		if !local.Before(open) && local.Before(close) {
			return nil
		}
	}
	return &CallbackOutsidePolicyError{Requested: requested}
}

func (p *RetryPolicy) nextBusinessTime(candidate time.Time) (time.Time, error) {
	base := candidate.In(p.location)
	for dayOffset := 0; dayOffset <= 7; dayOffset++ {
		day := base.AddDate(0, 0, dayOffset)
		for _, window := range p.windows[day.Weekday()] {
			open := p.localBoundary(day, window.Open)
			close := p.localBoundary(day, window.Close)
			if dayOffset == 0 && base.Before(open) {
				return open, nil
			}
			if base.Before(close) && !base.Before(open) {
				return base, nil
			}
			if dayOffset > 0 {
				return open, nil
			}
		}
	}
	return time.Time{}, &InvalidBusinessWindowError{Reason: "no next configured business window"}
}

func (p *RetryPolicy) localBoundary(day time.Time, offset time.Duration) time.Time {
	totalSeconds := int64(offset / time.Second)
	return time.Date(
		day.Year(),
		day.Month(),
		day.Day(),
		int(totalSeconds/3600),
		int((totalSeconds%3600)/60),
		int(totalSeconds%60),
		int(offset%time.Second),
		p.location,
	)
}
