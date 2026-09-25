package composiocalendar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testComposioKey = "composio-test-secret"

func calendarTestConfig(endpoint string) Config {
	return Config{APIKey: testComposioKey, UserID: "agentic-voice-sdr-test-user", BaseURL: endpoint + "/api/v3.1"}
}

func calendarEnvelope(data any, successful bool, providerError string) string {
	body, _ := json.Marshal(map[string]any{"successful": successful, "data": data, "error": providerError})
	return string(body)
}

func TestCheckAvailabilityMapsMinimalRequestAndIntersectsSlots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/GOOGLECALENDAR_FIND_FREE_SLOTS") {
			t.Errorf("provider request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != testComposioKey {
			t.Errorf("x-api-key missing")
		}
		var req struct {
			UserID    string                     `json:"user_id"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.UserID != "agentic-voice-sdr-test-user" {
			t.Errorf("user_id = %q", req.UserID)
		}
		var args struct {
			Items    []string `json:"items"`
			TimeMin  string   `json:"time_min"`
			TimeMax  string   `json:"time_max"`
			Timezone string   `json:"timezone"`
		}
		b, _ := json.Marshal(req.Arguments)
		_ = json.Unmarshal(b, &args)
		if strings.Join(args.Items, ",") != "primary,team@example.test" {
			t.Errorf("items = %v", args.Items)
		}
		if args.Timezone != "America/Los_Angeles" || !strings.Contains(args.TimeMin, "-08:00") || !strings.Contains(args.TimeMax, "-08:00") {
			t.Errorf("window/timezone = %+v", args)
		}
		if _, ok := req.Arguments["transcript"]; ok {
			t.Error("transcript sent to Composio")
		}
		response := map[string]any{"timeMin": args.TimeMin, "timeMax": args.TimeMax, "timeZone": args.Timezone, "calendars": map[string]any{
			"primary":           map[string]any{"is_reliable": true, "free": []any{map[string]string{"start": "2026-01-05T09:00:00-08:00", "end": "2026-01-05T12:00:00-08:00"}}, "busy": []any{}},
			"team@example.test": map[string]any{"is_reliable": true, "free": []any{map[string]string{"start": "2026-01-05T10:00:00-08:00", "end": "2026-01-05T11:00:00-08:00"}}, "busy": []any{}},
		}}
		_, _ = io.WriteString(w, calendarEnvelope(response, true, ""))
	}))
	defer server.Close()
	client, err := New(calendarTestConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-01-05T08:00:00-08:00")
	end, _ := time.Parse(time.RFC3339, "2026-01-05T13:00:00-08:00")
	got, err := client.CheckAvailability(context.Background(), AvailabilityRequest{Start: start, End: end, Timezone: "America/Los_Angeles", CalendarIDs: []string{"primary", "team@example.test"}, MinimumDuration: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Slots) != 1 || got.Slots[0].Start.Hour() != 10 || got.Slots[0].End.Hour() != 11 || got.Timezone != "America/Los_Angeles" {
		t.Fatalf("availability = %+v", got)
	}
}

func TestCheckAvailabilityEmptyAndInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     any
		expected error
	}{
		{name: "empty", data: map[string]any{"calendars": map[string]any{"primary": map[string]any{"free": []any{}, "busy": []any{}}}}},
		{name: "malformed shape", data: map[string]any{"unexpected": true}, expected: ErrCalendarInvalidResponse},
		{name: "unreliable calendar", data: map[string]any{"calendars": map[string]any{"primary": map[string]any{"is_reliable": false, "free": []any{}, "busy": []any{}}}}, expected: ErrCalendarInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, calendarEnvelope(tc.data, true, ""))
			}))
			defer server.Close()
			client, err := New(calendarTestConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
			got, err := client.CheckAvailability(context.Background(), AvailabilityRequest{Start: now, End: now.Add(time.Hour), Timezone: "UTC", CalendarIDs: []string{"primary"}})
			if tc.expected != nil {
				if !errors.Is(err, tc.expected) {
					t.Fatalf("error=%v want=%v", err, tc.expected)
				}
			} else if err != nil || len(got.Slots) != 0 {
				t.Fatalf("result=%+v error=%v", got, err)
			}
		})
	}
}

func TestCreateEventMapsTypedRequestAndSanitizesResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/GOOGLECALENDAR_CREATE_EVENT") {
			t.Errorf("path=%s", r.URL.Path)
		}
		var req struct {
			UserID    string         `json:"user_id"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		args := req.Arguments
		if args["timezone"] != "Europe/Paris" || args["calendar_id"] != "primary" || args["start_datetime"] != "2026-01-05T10:00:00" || args["end_datetime"] != "2026-01-05T10:45:00" || args["summary"] != "Discovery call" {
			t.Errorf("arguments=%+v", args)
		}
		if _, ok := args["description"]; ok {
			t.Error("description unexpectedly sent")
		}
		if _, ok := args["transcript"]; ok {
			t.Error("transcript sent")
		}
		if _, ok := req.Arguments["idempotency_key"]; ok {
			t.Error("invented idempotency key sent")
		}
		data := map[string]any{"response_data": map[string]any{"id": "event-123", "summary": "Discovery call", "start": map[string]string{"dateTime": "2026-01-05T10:00:00+01:00", "timeZone": "Europe/Paris"}, "end": map[string]string{"dateTime": "2026-01-05T10:45:00+01:00", "timeZone": "Europe/Paris"}, "htmlLink": "https://private.invalid/event"}}
		_, _ = io.WriteString(w, calendarEnvelope(data, true, ""))
	}))
	defer server.Close()
	client, err := New(calendarTestConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Paris")
	start := time.Date(2026, 1, 5, 10, 0, 0, 0, loc)
	end := start.Add(45 * time.Minute)
	got, err := client.CreateEvent(context.Background(), CreateEventRequest{Start: start, End: end, Timezone: "Europe/Paris", CalendarID: "primary", Summary: "Discovery call", Attendees: []string{"lead@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "event-123" || !got.Created || got.Timezone != "Europe/Paris" || !got.Start.Equal(start) || !got.End.Equal(end) {
		t.Fatalf("created event=%+v", got)
	}
	if strings.Contains(got.ID, "private.invalid") {
		t.Fatal("provider response leaked into canonical result")
	}
}

func TestCreateEventRejectsMalformedProviderResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, calendarEnvelope(map[string]any{"response_data": map[string]any{"summary": "missing id/time"}}, true, ""))
	}))
	defer server.Close()
	client, err := New(calendarTestConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	_, err = client.CreateEvent(context.Background(), CreateEventRequest{Start: start, End: start.Add(time.Hour), Timezone: "UTC", CalendarID: "primary", Summary: "Review"})
	if !errors.Is(err, ErrCalendarInvalidResponse) {
		t.Fatalf("error=%v", err)
	}
}

func TestProviderRejectionsAndSecretsAreSanitized(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "provider leaked "+testComposioKey+" PRIVATE-DETAIL")
			}))
			defer server.Close()
			client, err := New(calendarTestConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
			_, err = client.CheckAvailability(context.Background(), AvailabilityRequest{Start: now, End: now.Add(time.Hour), Timezone: "UTC", CalendarIDs: []string{"primary"}})
			if !errors.Is(err, ErrCalendarProviderRejected) || strings.Contains(err.Error(), testComposioKey) || strings.Contains(err.Error(), "PRIVATE-DETAIL") {
				t.Fatalf("provider error not sanitized: %v", err)
			}
		})
	}
}

func TestCalendarTimeoutAndCallerCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		name := "timeout"
		if cancelCaller {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
			defer server.Close()
			defer close(release)
			client, err := NewWithTimeout(calendarTestConfig(server.URL), 35*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
			done := make(chan error, 1)
			go func() {
				_, err := client.CheckAvailability(ctx, AvailabilityRequest{Start: now, End: now.Add(time.Hour), Timezone: "UTC", CalendarIDs: []string{"primary"}})
				done <- err
			}()
			<-started
			if cancelCaller {
				cancel()
			}
			select {
			case err := <-done:
				if cancelCaller && !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v want caller cancellation", err)
				}
				if !cancelCaller && !errors.Is(err, ErrCalendarTimeout) {
					t.Fatalf("error=%v want timeout", err)
				}
			case <-time.After(time.Second):
				t.Fatal("provider call did not end")
			}
		})
	}
}

func TestConfigurationRequiresSecretAndUser(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrCalendarConfiguration) {
		t.Fatalf("error=%v", err)
	}
}

func TestCreateEventProviderRejectionsAndSecretSanitization(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "provider details "+testComposioKey+" PERSONAL-DATA")
			}))
			defer server.Close()
			client, err := New(calendarTestConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
			_, err = client.CreateEvent(context.Background(), CreateEventRequest{Start: start, End: start.Add(time.Hour), Timezone: "UTC", CalendarID: "primary", Summary: "Discovery"})
			if !errors.Is(err, ErrCalendarProviderRejected) || strings.Contains(err.Error(), testComposioKey) || strings.Contains(err.Error(), "PERSONAL-DATA") {
				t.Fatalf("provider error not sanitized: %v", err)
			}
		})
	}
}

func TestCreateEventTimeoutAndCallerCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		name := "timeout"
		if cancelCaller {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			headersSent := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(headersSent)
				<-release
				_, _ = io.WriteString(w, calendarEnvelope(map[string]any{}, true, ""))
			}))
			defer server.Close()
			defer close(release)
			client, err := NewWithTimeout(calendarTestConfig(server.URL), 40*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
			done := make(chan error, 1)
			go func() {
				_, err := client.CreateEvent(ctx, CreateEventRequest{Start: start, End: start.Add(time.Hour), Timezone: "UTC", CalendarID: "primary", Summary: "Discovery"})
				done <- err
			}()
			<-headersSent
			if cancelCaller {
				cancel()
			}
			select {
			case err := <-done:
				if cancelCaller && !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v want caller cancellation", err)
				}
				if !cancelCaller && !errors.Is(err, ErrCalendarTimeout) {
					t.Fatalf("error=%v want timeout", err)
				}
			case <-time.After(time.Second):
				t.Fatal("create-event provider call did not end")
			}
		})
	}
}

func TestConfigFromEnvUsesComposioNamesAndDefaults(t *testing.T) {
	t.Setenv("COMPOSIO_API_KEY", "env-test-key")
	t.Setenv("COMPOSIO_USER_ID", "env-test-user")
	t.Setenv("COMPOSIO_BASE_URL", "")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.APIKey != "env-test-key" || config.UserID != "env-test-user" || config.BaseURL != defaultBaseURL {
		t.Fatalf("config = %+v", config)
	}
}

func TestComposioLogicalProviderErrorIsSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, calendarEnvelope(map[string]any{}, false, "provider said "+testComposioKey+" PRIVATE-DATA"))
	}))
	defer server.Close()
	client, err := New(calendarTestConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	_, err = client.CheckAvailability(context.Background(), AvailabilityRequest{Start: start, End: start.Add(time.Hour), Timezone: "UTC", CalendarIDs: []string{"primary"}})
	if !errors.Is(err, ErrCalendarProviderRejected) || strings.Contains(err.Error(), testComposioKey) || strings.Contains(err.Error(), "PRIVATE-DATA") {
		t.Fatalf("provider error not sanitized: %v", err)
	}
}

func TestAvailabilityRejectsMissingRequestedCalendar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]any{"calendars": map[string]any{"primary": map[string]any{"free": []any{map[string]string{"start": "2026-01-05T09:00:00Z", "end": "2026-01-05T10:00:00Z"}}, "busy": []any{}}, "other": map[string]any{"free": []any{map[string]string{"start": "2026-01-05T09:00:00Z", "end": "2026-01-05T10:00:00Z"}}, "busy": []any{}}}}
		_, _ = io.WriteString(w, calendarEnvelope(response, true, ""))
	}))
	defer server.Close()
	client, err := New(calendarTestConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	_, err = client.CheckAvailability(context.Background(), AvailabilityRequest{Start: start, End: start.Add(time.Hour), Timezone: "UTC", CalendarIDs: []string{"primary", "missing"}})
	if !errors.Is(err, ErrCalendarInvalidResponse) {
		t.Fatalf("error=%v", err)
	}
}
