// Package composiocalendar implements a narrow provider adapter for the two
// canonical calendar tools. It does not authorize, dispatch, or execute tools.
package composiocalendar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
)

const (
	defaultBaseURL        = "https://backend.composio.dev/api/v3.1"
	defaultTimeout        = 10 * time.Second
	maxResponseSize       = 1 << 20
	maxAvailabilityWindow = 90 * 24 * time.Hour
)

const (
	findFreeSlotsSlug = "GOOGLECALENDAR_FIND_FREE_SLOTS"
	createEventSlug   = "GOOGLECALENDAR_CREATE_EVENT"

	// CanonicalToolCheckAvailability and CanonicalToolCreateEvent are aliases
	// of the registry IDs, not a second registry or runtime dispatcher.
	CanonicalToolCheckAvailability = tools.ToolCalendarCheckAvailability
	CanonicalToolCreateEvent       = tools.ToolCalendarCreateEvent
)

var (
	ErrCalendarConfiguration    = errors.New("Composio Calendar configuration is invalid")
	ErrCalendarTimeout          = errors.New("Composio Calendar request timed out")
	ErrCalendarTransport        = errors.New("Composio Calendar transport failed")
	ErrCalendarProviderRejected = errors.New("Composio Calendar provider rejected the request")
	ErrCalendarInvalidResponse  = errors.New("Composio Calendar returned an invalid response")
)

type Config struct {
	APIKey  string
	UserID  string
	BaseURL string
}

// ConfigFromEnv uses Composio's current project-key and user_id terminology.
func ConfigFromEnv() (Config, error) {
	return NewConfig(Config{APIKey: os.Getenv("COMPOSIO_API_KEY"), UserID: os.Getenv("COMPOSIO_USER_ID"), BaseURL: os.Getenv("COMPOSIO_BASE_URL")})
}

func NewConfig(config Config) (Config, error) {
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.UserID = strings.TrimSpace(config.UserID)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.APIKey == "" || config.UserID == "" {
		return Config{}, ErrCalendarConfiguration
	}
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, ErrCalendarConfiguration
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return Config{}, ErrCalendarConfiguration
		}
	}
	return config, nil
}

type Client struct {
	config     Config
	httpClient *http.Client
	timeout    time.Duration
}

func New(config Config) (*Client, error) { return NewWithTimeout(config, defaultTimeout) }

func NewWithTimeout(config Config, timeout time.Duration) (*Client, error) {
	validated, err := NewConfig(config)
	if err != nil || timeout <= 0 {
		return nil, ErrCalendarConfiguration
	}
	return &Client{config: validated, httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, timeout: timeout}, nil
}

type AvailabilityRequest struct {
	Start           time.Time
	End             time.Time
	Timezone        string
	CalendarIDs     []string
	MinimumDuration time.Duration
}

type AvailableSlot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type AvailabilityResult struct {
	Slots    []AvailableSlot `json:"slots"`
	Timezone string          `json:"timezone"`
}

type CreateEventRequest struct {
	Start      time.Time
	End        time.Time
	Timezone   string
	CalendarID string
	Summary    string
	Attendees  []string
}

type CreatedEvent struct {
	Created  bool      `json:"created"`
	ID       string    `json:"event_id"`
	Summary  string    `json:"summary,omitempty"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Timezone string    `json:"timezone"`
}

type executeRequest struct {
	UserID    string `json:"user_id"`
	Arguments any    `json:"arguments"`
}

type executeEnvelope struct {
	Successful *bool           `json:"successful"`
	Error      json.RawMessage `json:"error"`
	Data       json.RawMessage `json:"data"`
}

func (c *Client) CheckAvailability(ctx context.Context, request AvailabilityRequest) (AvailabilityResult, error) {
	loc, err := validateWindow(request.Start, request.End, request.Timezone)
	if err != nil || len(request.CalendarIDs) == 0 || request.MinimumDuration < 0 || request.End.Sub(request.Start) > maxAvailabilityWindow {
		return AvailabilityResult{}, ErrCalendarConfiguration
	}
	seen := make(map[string]struct{}, len(request.CalendarIDs))
	items := make([]string, 0, len(request.CalendarIDs))
	for _, id := range request.CalendarIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return AvailabilityResult{}, ErrCalendarConfiguration
		}
		if _, exists := seen[id]; exists {
			return AvailabilityResult{}, ErrCalendarConfiguration
		}
		seen[id] = struct{}{}
		items = append(items, id)
	}
	args := struct {
		Items    []string `json:"items"`
		TimeMin  string   `json:"time_min"`
		TimeMax  string   `json:"time_max"`
		Timezone string   `json:"timezone"`
	}{Items: items, TimeMin: request.Start.In(loc).Format(time.RFC3339), TimeMax: request.End.In(loc).Format(time.RFC3339), Timezone: request.Timezone}
	data, err := c.execute(ctx, findFreeSlotsSlug, args)
	if err != nil {
		return AvailabilityResult{}, err
	}
	result, err := parseAvailability(data, items, loc, request.Timezone, request.Start, request.End)
	if err != nil {
		return AvailabilityResult{}, ErrCalendarInvalidResponse
	}
	if request.MinimumDuration > 0 {
		filtered := result.Slots[:0]
		for _, slot := range result.Slots {
			if slot.End.Sub(slot.Start) >= request.MinimumDuration {
				filtered = append(filtered, slot)
			}
		}
		result.Slots = filtered
	}
	return result, nil
}

func (c *Client) CreateEvent(ctx context.Context, request CreateEventRequest) (CreatedEvent, error) {
	loc, err := validateWindow(request.Start, request.End, request.Timezone)
	if err != nil || strings.TrimSpace(request.CalendarID) == "" || strings.TrimSpace(request.Summary) == "" || !request.End.After(request.Start) {
		return CreatedEvent{}, ErrCalendarConfiguration
	}
	attendees := make([]string, 0, len(request.Attendees))
	for _, attendee := range request.Attendees {
		attendee = strings.TrimSpace(attendee)
		if attendee == "" || strings.ContainsAny(attendee, "\r\n") {
			return CreatedEvent{}, ErrCalendarConfiguration
		}
		attendees = append(attendees, attendee)
	}
	args := struct {
		CalendarID string   `json:"calendar_id"`
		Start      string   `json:"start_datetime"`
		End        string   `json:"end_datetime"`
		Timezone   string   `json:"timezone"`
		Summary    string   `json:"summary"`
		Attendees  []string `json:"attendees,omitempty"`
	}{CalendarID: strings.TrimSpace(request.CalendarID), Start: request.Start.In(loc).Format("2006-01-02T15:04:05"), End: request.End.In(loc).Format("2006-01-02T15:04:05"), Timezone: request.Timezone, Summary: strings.TrimSpace(request.Summary), Attendees: attendees}
	data, err := c.execute(ctx, createEventSlug, args)
	if err != nil {
		return CreatedEvent{}, err
	}
	result, err := parseCreatedEvent(data, request.Timezone)
	if err != nil {
		return CreatedEvent{}, ErrCalendarInvalidResponse
	}
	return result, nil
}

func (c *Client) execute(ctx context.Context, slug string, arguments any) (json.RawMessage, error) {
	if ctx == nil {
		return nil, ErrCalendarConfiguration
	}
	body, err := json.Marshal(executeRequest{UserID: c.config.UserID, Arguments: arguments})
	if err != nil {
		return nil, ErrCalendarConfiguration
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	endpoint := c.config.BaseURL + "/tools/execute/" + url.PathEscape(slug)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrCalendarConfiguration
	}
	req.Header.Set("x-api-key", c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, classifyRequestError(ctx, requestCtx)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusGatewayTimeout {
		return nil, ErrCalendarTimeout
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrCalendarProviderRejected
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, classifyRequestError(ctx, requestCtx)
	}
	if len(responseBody) == 0 || len(responseBody) > maxResponseSize {
		return nil, ErrCalendarInvalidResponse
	}
	var envelope executeEnvelope
	if json.Unmarshal(responseBody, &envelope) != nil || envelope.Successful == nil || envelope.Data == nil || string(envelope.Data) == "null" {
		return nil, ErrCalendarInvalidResponse
	}
	if !*envelope.Successful || rawValuePresent(envelope.Error) {
		return nil, ErrCalendarProviderRejected
	}
	return envelope.Data, nil
}

func classifyRequestError(ctx, requestCtx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
		return ErrCalendarTimeout
	}
	return ErrCalendarTransport
}

func rawValuePresent(value json.RawMessage) bool {
	if len(value) == 0 || string(value) == "null" {
		return false
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return true
	}
	switch typed := decoded.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	default:
		return decoded != nil
	}
}

func validateWindow(start, end time.Time, timezone string) (*time.Location, error) {
	if timezone == "" {
		return nil, ErrCalendarConfiguration
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil || start.IsZero() || end.IsZero() || !end.After(start) {
		return nil, ErrCalendarConfiguration
	}
	return loc, nil
}

type availabilityData struct {
	Calendars map[string]calendarAvailability `json:"calendars"`
}
type calendarAvailability struct {
	Free     []providerInterval `json:"free"`
	Busy     []providerInterval `json:"busy"`
	Errors   json.RawMessage    `json:"errors"`
	Reliable *bool              `json:"is_reliable"`
}
type providerInterval struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func parseAvailability(data json.RawMessage, requestedCalendars []string, loc *time.Location, timezone string, windowStart, windowEnd time.Time) (AvailabilityResult, error) {
	var parsed availabilityData
	if json.Unmarshal(data, &parsed) != nil {
		return AvailabilityResult{}, ErrCalendarInvalidResponse
	}
	var intersection []AvailableSlot
	first := true
	for _, calendarID := range requestedCalendars {
		calendar, exists := parsed.Calendars[calendarID]
		if !exists || rawValuePresent(calendar.Errors) || (calendar.Reliable != nil && !*calendar.Reliable) {
			return AvailabilityResult{}, ErrCalendarInvalidResponse
		}
		free, err := parseIntervals(calendar.Free, loc)
		if err != nil {
			return AvailabilityResult{}, err
		}
		free = clipSlots(free, windowStart, windowEnd)
		if _, err := parseIntervals(calendar.Busy, loc); err != nil {
			return AvailabilityResult{}, err
		}
		if first {
			intersection = free
			first = false
		} else {
			intersection = intersectSlots(intersection, free)
		}
	}
	for i := range intersection {
		intersection[i].Start = intersection[i].Start.In(loc)
		intersection[i].End = intersection[i].End.In(loc)
	}
	sort.Slice(intersection, func(i, j int) bool { return intersection[i].Start.Before(intersection[j].Start) })
	return AvailabilityResult{Slots: intersection, Timezone: timezone}, nil
}

func parseIntervals(intervals []providerInterval, loc *time.Location) ([]AvailableSlot, error) {
	result := make([]AvailableSlot, 0, len(intervals))
	for _, interval := range intervals {
		start, err := parseProviderTime(interval.Start, loc)
		if err != nil {
			return nil, err
		}
		end, err := parseProviderTime(interval.End, loc)
		if err != nil || !end.After(start) {
			return nil, ErrCalendarInvalidResponse
		}
		result = append(result, AvailableSlot{Start: start, End: end})
	}
	return result, nil
}

func parseProviderTime(value string, loc *time.Location) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if parsed, err := time.ParseInLocation(layout, value, loc); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, ErrCalendarInvalidResponse
}

func clipSlots(slots []AvailableSlot, windowStart, windowEnd time.Time) []AvailableSlot {
	clipped := make([]AvailableSlot, 0, len(slots))
	for _, slot := range slots {
		if slot.Start.Before(windowStart) {
			slot.Start = windowStart
		}
		if slot.End.After(windowEnd) {
			slot.End = windowEnd
		}
		if slot.End.After(slot.Start) {
			clipped = append(clipped, slot)
		}
	}
	return clipped
}

func intersectSlots(left, right []AvailableSlot) []AvailableSlot {
	var intersections []AvailableSlot
	for _, a := range left {
		for _, b := range right {
			start := a.Start
			if b.Start.After(start) {
				start = b.Start
			}
			end := a.End
			if b.End.Before(end) {
				end = b.End
			}
			if end.After(start) {
				intersections = append(intersections, AvailableSlot{Start: start, End: end})
			}
		}
	}
	return intersections
}

type providerEvent struct {
	ID      string            `json:"id"`
	Summary string            `json:"summary"`
	Start   providerEventTime `json:"start"`
	End     providerEventTime `json:"end"`
}
type providerEventTime struct {
	DateTime string `json:"dateTime"`
	Timezone string `json:"timeZone"`
}
type eventData struct {
	ResponseData *providerEvent    `json:"response_data"`
	Event        *providerEvent    `json:"event"`
	ID           string            `json:"id"`
	Summary      string            `json:"summary"`
	Start        providerEventTime `json:"start"`
	End          providerEventTime `json:"end"`
}

func parseCreatedEvent(data json.RawMessage, requestedTimezone string) (CreatedEvent, error) {
	var wrapper eventData
	if json.Unmarshal(data, &wrapper) != nil {
		return CreatedEvent{}, ErrCalendarInvalidResponse
	}
	event := wrapper.ResponseData
	if event == nil {
		event = wrapper.Event
	}
	if event == nil && wrapper.ID != "" {
		event = &providerEvent{ID: wrapper.ID, Summary: wrapper.Summary, Start: wrapper.Start, End: wrapper.End}
	}
	if event == nil || strings.TrimSpace(event.ID) == "" {
		return CreatedEvent{}, ErrCalendarInvalidResponse
	}
	startTimezone := strings.TrimSpace(event.Start.Timezone)
	endTimezone := strings.TrimSpace(event.End.Timezone)
	if startTimezone != "" && endTimezone != "" && startTimezone != endTimezone {
		return CreatedEvent{}, ErrCalendarInvalidResponse
	}
	timezone := startTimezone
	if timezone == "" {
		timezone = endTimezone
	}
	if timezone == "" {
		timezone = requestedTimezone
	}
	responseLoc, err := time.LoadLocation(timezone)
	if err != nil {
		return CreatedEvent{}, ErrCalendarInvalidResponse
	}
	start, err := parseProviderTime(event.Start.DateTime, responseLoc)
	if err != nil {
		return CreatedEvent{}, ErrCalendarInvalidResponse
	}
	end, err := parseProviderTime(event.End.DateTime, responseLoc)
	if err != nil || !end.After(start) {
		return CreatedEvent{}, ErrCalendarInvalidResponse
	}
	start = start.In(responseLoc)
	end = end.In(responseLoc)
	return CreatedEvent{Created: true, ID: event.ID, Summary: event.Summary, Start: start, End: end, Timezone: timezone}, nil
}
