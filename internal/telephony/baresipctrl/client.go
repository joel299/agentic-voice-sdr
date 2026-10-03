// Package baresipctrl implements Baresip's local ctrl_tcp JSON/netstring API.
package baresipctrl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

var (
	ErrInvalidAddress     = errors.New("baresip ctrl_tcp: address must be a loopback TCP address")
	ErrNotStarted         = errors.New("baresip ctrl_tcp: client not started")
	ErrAlreadyStarted     = errors.New("baresip ctrl_tcp: client already started")
	ErrClientClosed       = errors.New("baresip ctrl_tcp: client closed")
	ErrDisconnected       = errors.New("baresip ctrl_tcp: connection lost; command was not retried")
	ErrPendingLimit       = errors.New("baresip ctrl_tcp: pending command limit reached")
	ErrFrameTooLarge      = errors.New("baresip ctrl_tcp: frame exceeds configured limit")
	ErrInvalidFrame       = errors.New("baresip ctrl_tcp: invalid netstring frame")
	ErrInvalidMessage     = errors.New("baresip ctrl_tcp: invalid JSON message")
	ErrCommandRejected    = errors.New("baresip ctrl_tcp: command rejected")
	ErrUnsupportedCommand = errors.New("baresip ctrl_tcp: command outside outbound control allowlist")
	sipStatusCode         = regexp.MustCompile(`\b([1-6][0-9]{2})\b`)
)

type ContextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type Options struct {
	Address        string
	EventBuffer    int
	MaxPending     int
	MaxCallStates  int
	MaxFrameSize   int
	WriteTimeout   time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Dialer         ContextDialer
}

type Client struct {
	address        string
	maxPending     int
	maxCallStates  int
	maxFrameSize   int
	writeTimeout   time.Duration
	initialBackoff time.Duration
	maxBackoff     time.Duration
	dialer         ContextDialer

	mu            sync.Mutex
	conn          net.Conn
	stateChanged  chan struct{}
	pending       map[string]chan commandOutcome
	registration  control.RegistrationStatus
	callStates    map[string]callLifecycle
	callSequence  uint64
	started       bool
	closed        bool
	cancel        context.CancelFunc
	runDone       chan struct{}
	events        chan control.Event
	eventClose    sync.Once
	writeMu       sync.Mutex
	tokenCounter  atomic.Uint64
	droppedEvents atomic.Uint64
}

type callLifecycle struct {
	stages   callStages
	sequence uint64
}

type callStages uint8

const (
	callStageOutgoing callStages = 1 << iota
	callStageProgress
	callStageRinging
	callStageConnected
)

type commandOutcome struct {
	result control.CommandResult
	err    error
}

type wireRequest struct {
	Command string `json:"command"`
	Params  string `json:"params"`
	Token   string `json:"token"`
}

type wireMessage struct {
	Response  boolish `json:"response"`
	Event     boolish `json:"event"`
	Message   boolish `json:"message"`
	OK        boolish `json:"ok"`
	Data      string  `json:"data"`
	Token     string  `json:"token"`
	Class     string  `json:"class"`
	Type      string  `json:"type"`
	Param     string  `json:"param"`
	CallID    string  `json:"id"`
	PeerURI   string  `json:"peeruri"`
	Direction string  `json:"direction"`
}

type boolish bool

var ansiEscape = regexp.MustCompile("\\x1b\\[[0-?]*[ -/]*[@-~]")

func (b *boolish) UnmarshalJSON(data []byte) error {
	var value bool
	if err := json.Unmarshal(data, &value); err == nil {
		*b = boolish(value)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	*b = boolish(strings.EqualFold(text, "true"))
	return nil
}

// New creates a client. ctrl_tcp is intentionally restricted to loopback;
// the Baresip listener is a local control plane and must not be remotely bound.
func New(options Options) (*Client, error) {
	host, _, err := net.SplitHostPort(options.Address)
	if err != nil {
		return nil, ErrInvalidAddress
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return nil, ErrInvalidAddress
	}
	if options.EventBuffer <= 0 {
		options.EventBuffer = 64
	}
	if options.MaxPending <= 0 {
		options.MaxPending = 64
	}
	if options.MaxCallStates <= 0 {
		options.MaxCallStates = 128
	}
	if options.MaxFrameSize <= 0 {
		options.MaxFrameSize = 1 << 20
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = 5 * time.Second
	}
	if options.InitialBackoff <= 0 {
		options.InitialBackoff = 250 * time.Millisecond
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = 5 * time.Second
	}
	if options.MaxBackoff < options.InitialBackoff {
		options.MaxBackoff = options.InitialBackoff
	}
	if options.Dialer == nil {
		options.Dialer = &net.Dialer{Timeout: options.WriteTimeout}
	}
	return &Client{
		address:        options.Address,
		maxPending:     options.MaxPending,
		maxCallStates:  options.MaxCallStates,
		maxFrameSize:   options.MaxFrameSize,
		writeTimeout:   options.WriteTimeout,
		initialBackoff: options.InitialBackoff,
		maxBackoff:     options.MaxBackoff,
		dialer:         options.Dialer,
		stateChanged:   make(chan struct{}),
		pending:        make(map[string]chan commandOutcome),
		callStates:     make(map[string]callLifecycle),
		runDone:        make(chan struct{}),
		events:         make(chan control.Event, options.EventBuffer),
	}, nil
}

// Start launches the persistent connection and waits until the first local
// TCP connection is established. Reconnect attempts continue in the background.
func (c *Client) Start(ctx context.Context) error {
	if c == nil {
		return ErrClientClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClientClosed
	}
	if c.started {
		c.mu.Unlock()
		return ErrAlreadyStarted
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.started = true
	c.mu.Unlock()

	go c.run(runCtx)
	if err := c.WaitConnected(ctx); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

// WaitConnected blocks until the client has an active ctrl_tcp connection.
func (c *Client) WaitConnected(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		if c.conn != nil {
			c.mu.Unlock()
			return nil
		}
		if c.closed {
			c.mu.Unlock()
			return ErrClientClosed
		}
		if !c.started {
			c.mu.Unlock()
			return ErrNotStarted
		}
		changed := c.stateChanged
		c.mu.Unlock()
		select {
		case <-changed:
		case <-c.runDone:
			return ErrClientClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Run maintains the persistent TCP connection until ctx is canceled. Start is
// normally preferred because it also waits for the first successful connection.
func (c *Client) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.runDone:
		return nil
	}
}

func (c *Client) run(ctx context.Context) {
	defer func() {
		c.mu.Lock()
		c.closed = true
		c.signalLocked()
		c.mu.Unlock()
		c.eventClose.Do(func() { close(c.events) })
		close(c.runDone)
	}()

	backoff := c.initialBackoff
	for ctx.Err() == nil {
		conn, err := c.dialer.DialContext(ctx, "tcp", c.address)
		if err != nil {
			if !waitContext(ctx, backoff) {
				return
			}
			backoff = growBackoff(backoff, c.maxBackoff)
			continue
		}
		if !c.setConn(conn) {
			_ = conn.Close()
			return
		}
		backoff = c.initialBackoff
		stopConnWatch := context.AfterFunc(ctx, func() { _ = conn.Close() })
		_ = c.readLoop(ctx, conn)
		stopConnWatch()
		c.disconnect(conn)
		_ = conn.Close()
		if ctx.Err() != nil {
			return
		}
		if !waitContext(ctx, backoff) {
			return
		}
		backoff = growBackoff(backoff, c.maxBackoff)
	}
}

func (c *Client) setConn(conn net.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.conn = conn
	c.signalLocked()
	return true
}

func (c *Client) disconnect(conn net.Conn) {
	c.mu.Lock()
	if c.conn != conn {
		c.mu.Unlock()
		return
	}
	c.conn = nil
	for token, response := range c.pending {
		response <- commandOutcome{err: ErrDisconnected}
		delete(c.pending, token)
	}
	c.signalLocked()
	c.mu.Unlock()
}

func (c *Client) readLoop(ctx context.Context, conn net.Conn) error {
	reader := bufio.NewReader(conn)
	for {
		payload, err := readNetstring(reader, c.maxFrameSize)
		if err != nil {
			return err
		}
		var message wireMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			return ErrInvalidMessage
		}
		switch {
		case bool(message.Response):
			c.deliverResponse(message)
		case bool(message.Event):
			c.publishEvent(c.normalizeLifecycleEvent(normalizeEvent(message)))
		case bool(message.Message):
			event := normalizeEvent(message)
			if event.Type == "" {
				event.Type = "MESSAGE"
			}
			c.publishEvent(event)
		default:
			return ErrInvalidMessage
		}
	}
}

func (c *Client) deliverResponse(message wireMessage) {
	if message.Token == "" {
		return
	}
	c.mu.Lock()
	response := c.pending[message.Token]
	if response != nil {
		delete(c.pending, message.Token)
	}
	c.mu.Unlock()
	if response != nil {
		response <- commandOutcome{result: control.CommandResult{Token: message.Token, Data: message.Data, OK: bool(message.OK)}, err: nil}
	}
}

// Do sends one command and waits for its correlated response. Commands are
// never replayed after a disconnect; callers must explicitly decide whether
// to issue a new command.
func (c *Client) Do(ctx context.Context, command, params string) (control.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command = strings.TrimSpace(command)
	stateChanging := command == "dial" || command == "hangup"
	commandError := func(err error, certainty control.DispatchCertainty) error {
		if err == nil || !stateChanging {
			return err
		}
		return &control.CommandError{Certainty: certainty, Cause: err}
	}
	if command == "" || strings.ContainsAny(command+params, "\r\n\x00") {
		return control.CommandResult{}, commandError(ErrInvalidMessage, control.DispatchNotDispatched)
	}
	switch command {
	case "reginfo", "dial", "hangup", "listcalls", "gru151_media_stats":
	default:
		return control.CommandResult{}, commandError(ErrUnsupportedCommand, control.DispatchNotDispatched)
	}
	conn, err := c.waitConn(ctx)
	if err != nil {
		return control.CommandResult{}, commandError(err, control.DispatchNotDispatched)
	}
	token := strconv.FormatUint(c.tokenCounter.Add(1), 10)
	request := wireRequest{Command: command, Params: params, Token: token}
	payload, err := json.Marshal(request)
	if err != nil {
		return control.CommandResult{}, commandError(ErrInvalidMessage, control.DispatchNotDispatched)
	}
	if len(payload) > c.maxFrameSize {
		return control.CommandResult{}, commandError(ErrFrameTooLarge, control.DispatchNotDispatched)
	}
	response := make(chan commandOutcome, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return control.CommandResult{}, commandError(ErrClientClosed, control.DispatchNotDispatched)
	}
	if c.conn != conn {
		c.mu.Unlock()
		return control.CommandResult{}, commandError(ErrDisconnected, control.DispatchNotDispatched)
	}
	if len(c.pending) >= c.maxPending {
		c.mu.Unlock()
		return control.CommandResult{}, commandError(ErrPendingLimit, control.DispatchNotDispatched)
	}
	c.pending[token] = response
	c.mu.Unlock()

	c.writeMu.Lock()
	if !c.isCurrentConn(conn) {
		c.writeMu.Unlock()
		c.removePending(token)
		return control.CommandResult{}, commandError(ErrDisconnected, control.DispatchNotDispatched)
	}
	written, err := c.writeCommand(ctx, conn, payload)
	if err != nil {
		c.writeMu.Unlock()
		c.removePending(token)
		_ = conn.Close()
		certainty := control.DispatchNotDispatched
		if written > 0 {
			certainty = control.DispatchMaybeDispatched
		}
		return control.CommandResult{}, commandError(err, certainty)
	}
	c.writeMu.Unlock()

	select {
	case outcome := <-response:
		return outcome.result, commandError(outcome.err, control.DispatchMaybeDispatched)
	case <-ctx.Done():
		c.removePending(token)
		return control.CommandResult{}, commandError(ctx.Err(), control.DispatchMaybeDispatched)
	case <-c.runDone:
		c.removePending(token)
		return control.CommandResult{}, commandError(ErrClientClosed, control.DispatchMaybeDispatched)
	}
}

func (c *Client) waitConn(ctx context.Context) (net.Conn, error) {
	for {
		c.mu.Lock()
		if c.conn != nil {
			conn := c.conn
			c.mu.Unlock()
			return conn, nil
		}
		if c.closed {
			c.mu.Unlock()
			return nil, ErrClientClosed
		}
		if !c.started {
			c.mu.Unlock()
			return nil, ErrNotStarted
		}
		changed := c.stateChanged
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.runDone:
			return nil, ErrClientClosed
		}
	}
}

func (c *Client) writeCommand(ctx context.Context, conn net.Conn, payload []byte) (int, error) {
	deadline := time.Now().Add(c.writeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return 0, ErrDisconnected
	}
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetWriteDeadline(time.Now())
		close(callbackDone)
	})
	written, err := writeNetstring(conn, payload)
	if !stop() {
		<-callbackDone
	}
	_ = conn.SetWriteDeadline(time.Time{})
	if err != nil {
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		return written, ErrDisconnected
	}
	return written, nil
}

func (c *Client) isCurrentConn(conn net.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.conn == conn
}

func (c *Client) removePending(token string) {
	c.mu.Lock()
	delete(c.pending, token)
	c.mu.Unlock()
}

func (c *Client) signalLocked() {
	close(c.stateChanged)
	c.stateChanged = make(chan struct{})
}

func (c *Client) RegistrationStatus(ctx context.Context) (control.RegistrationStatus, error) {
	result, err := c.Do(ctx, "reginfo", "")
	if err != nil {
		return control.RegistrationStatus{}, err
	}
	if !result.OK {
		return control.RegistrationStatus{State: control.RegistrationFailed, Detail: "registration status command rejected"}, ErrCommandRejected
	}
	detail := strings.TrimSpace(ansiEscape.ReplaceAllString(result.Data, ""))
	value := strings.ToLower(detail)
	state := control.RegistrationUnknown
	userAgents, hasUserAgentList := reginfoUserAgentCount(detail)
	switch {
	case strings.Contains(value, "unregistered"), strings.Contains(value, "not registered"), hasUserAgentList && userAgents == 0:
		state = control.RegistrationNotRegistered
	case strings.Contains(value, "registered"), strings.Contains(value, "binding"), strings.Contains(value, "200 ok"), reginfoHasActiveUserAgent(detail):
		state = control.RegistrationRegistered
	case strings.Contains(value, "registration failed"), strings.Contains(value, "registration error"):
		state = control.RegistrationFailed
	}
	if state == control.RegistrationUnknown {
		c.mu.Lock()
		observed := c.registration
		c.mu.Unlock()
		if observed.State != control.RegistrationUnknown {
			return observed, nil
		}
	}
	return control.RegistrationStatus{State: state, Detail: registrationStateDetail(state)}, nil
}

func reginfoHasActiveUserAgent(detail string) bool {
	count, ok := reginfoUserAgentCount(detail)
	if !ok || count < 1 {
		return false
	}
	value := strings.ToLower(detail)
	header := strings.Index(value, "user agents (")
	start := header + len("user agents (")
	endOffset := strings.IndexByte(value[start:], ')')
	for _, line := range strings.Split(value[start+endOffset+1:], "\n") {
		if strings.Contains(line, "sip:") && strings.Contains(line, "expires") && hasWord(line, "ok") {
			return true
		}
	}
	return false
}

func reginfoUserAgentCount(detail string) (int, bool) {
	value := strings.ToLower(detail)
	header := strings.Index(value, "user agents (")
	if header < 0 {
		return 0, false
	}
	start := header + len("user agents (")
	endOffset := strings.IndexByte(value[start:], ')')
	if endOffset < 0 {
		return 0, false
	}
	count, err := strconv.Atoi(strings.TrimSpace(value[start : start+endOffset]))
	if err != nil || count < 0 {
		return 0, false
	}
	return count, true
}

func hasWord(value, target string) bool {
	for _, word := range strings.Fields(value) {
		if strings.Trim(word, "[](),;:") == target {
			return true
		}
	}
	return false
}

func registrationStateDetail(state control.RegistrationState) string {
	switch state {
	case control.RegistrationRegistered:
		return "registered"
	case control.RegistrationNotRegistered:
		return "not registered"
	case control.RegistrationFailed:
		return "registration failed"
	default:
		return "registration status unknown"
	}
}

func (c *Client) Dial(ctx context.Context, destination string) (control.CommandResult, error) {
	if strings.TrimSpace(destination) == "" {
		return control.CommandResult{}, &control.CommandError{Certainty: control.DispatchNotDispatched, Cause: ErrInvalidMessage}
	}
	return c.command(ctx, "dial", destination)
}

func (c *Client) Hangup(ctx context.Context) (control.CommandResult, error) {
	return c.command(ctx, "hangup", "")
}

func (c *Client) ListCalls(ctx context.Context) (control.CommandResult, error) {
	return c.command(ctx, "listcalls", "")
}

func (c *Client) ActiveCalls(ctx context.Context) ([]control.ActiveCall, error) {
	result, err := c.ListCalls(ctx)
	if err != nil {
		return nil, err
	}
	return ParseActiveCalls(result.Data)
}

func (c *Client) command(ctx context.Context, name, params string) (control.CommandResult, error) {
	result, err := c.Do(ctx, name, params)
	if err != nil {
		return control.CommandResult{}, err
	}
	if !result.OK {
		certainty := control.DispatchMaybeDispatched
		if name == "dial" || name == "hangup" {
			certainty = control.DispatchRejected
		}
		return result, &control.CommandError{Certainty: certainty, Cause: ErrCommandRejected}
	}
	return result, nil
}

func (c *Client) Events() <-chan control.Event {
	if c == nil {
		return nil
	}
	return c.events
}

// DroppedEventCount reports events discarded because the bounded Events
// buffer was full. The socket reader never waits for an event consumer, so
// command responses remain readable under event backpressure.
func (c *Client) DroppedEventCount() uint64 {
	if c == nil {
		return 0
	}
	return c.droppedEvents.Load()
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		started := c.started
		runDone := c.runDone
		c.mu.Unlock()
		if started {
			<-runDone
		}
		return nil
	}
	c.closed = true
	cancel := c.cancel
	conn := c.conn
	started := c.started
	c.signalLocked()
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	if started {
		<-c.runDone
	} else {
		c.eventClose.Do(func() { close(c.events) })
		close(c.runDone)
	}
	return nil
}

func normalizeEvent(message wireMessage) control.Event {
	event := control.Event{Class: message.Class, Type: message.Type, CallID: message.CallID, PeerURI: message.PeerURI, Direction: message.Direction, Param: message.Param}
	switch strings.ToUpper(message.Type) {
	case "AUDIO_ERROR":
		event.Param = safeAudioErrorClass(message.Param)
	case "CALL_OUTGOING", "CALL_SETUP":
		event.State = control.CallStateOutgoing
	case "CALL_PROGRESS", "CALL_SESSION_PROGRESS":
		event.State = control.CallStateProgress
	case "CALL_RINGING", "CALL_ALERTING":
		event.State = control.CallStateRinging
	case "CALL_ESTABLISHED", "CALL_ANSWERED":
		event.State = control.CallStateConnected
	case "CALL_CLOSED", "CALL_TERMINATED":
		event.State = control.CallStateUnknown
	case "CALL_FAILED":
		event.State = control.CallStateFailed
	case "REGISTERING":
		event.RegistrationState = control.RegistrationRegistering
	case "REGISTER_OK":
		event.RegistrationState = control.RegistrationRegistered
	case "REGISTER_FAIL":
		event.RegistrationState = control.RegistrationFailed
	case "UNREGISTERING":
		event.RegistrationState = control.RegistrationNotRegistered
	default:
		event.State = control.CallStateUnknown
	}
	return event
}

func (c *Client) publishEvent(event control.Event) {
	select {
	case c.events <- event:
	default:
		c.droppedEvents.Add(1)
	}
}

func (c *Client) normalizeLifecycleEvent(event control.Event) control.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	if event.RegistrationState != control.RegistrationUnknown {
		c.registration = control.RegistrationStatus{State: event.RegistrationState, Detail: event.Param}
	}
	if !strings.EqualFold(event.Class, "call") || event.CallID == "" {
		return event
	}
	switch strings.ToUpper(event.Type) {
	case "CALL_CLOSED", "CALL_TERMINATED":
		previous, seen := c.callStates[event.CallID]
		connected := seen && previous.stages&callStageConnected != 0
		event.State, event.Param = classifyCallClose(event.Param, connected)
		delete(c.callStates, event.CallID)
	case "CALL_FAILED":
		event.State, event.Param = classifyCallFailure(event.Param)
		delete(c.callStates, event.CallID)
	default:
		stage := callStageForEvent(event.Type)
		if stage == 0 || event.State == control.CallStateUnknown {
			return event
		}
		previous, seen := c.callStates[event.CallID]
		c.callSequence++
		if !seen && len(c.callStates) >= c.maxCallStates {
			c.evictOldestCallLocked()
		}
		c.callStates[event.CallID] = callLifecycle{stages: previous.stages | stage, sequence: c.callSequence}
	}
	return event
}

func classifyCallFailure(param string) (control.CallState, string) {
	state, reason := classifyCallClose(param, false)
	switch state {
	case control.CallStateBusy, control.CallStateNoAnswer, control.CallStateCanceled:
		return state, reason
	}
	for _, match := range sipStatusCode.FindAllStringSubmatch(param, -1) {
		if len(match) != 2 {
			continue
		}
		code, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		switch {
		case code >= 400 && code < 500:
			return control.CallStateFailed, "sip_" + strconv.Itoa(code)
		case code >= 500 && code < 600:
			return control.CallStateFailed, "sip_" + strconv.Itoa(code)
		}
	}
	value := strings.ToLower(strings.TrimSpace(param))
	if containsAny(value, "connection reset", "connection refused", "transport error", "network unreachable", "dns") {
		return control.CallStateFailed, "transport_error"
	}
	if value == "" || reason == "unknown" {
		return control.CallStateFailed, "unknown"
	}
	return control.CallStateFailed, "failed"
}

func callStageForEvent(eventType string) callStages {
	switch strings.ToUpper(eventType) {
	case "CALL_OUTGOING", "CALL_SETUP":
		return callStageOutgoing
	case "CALL_PROGRESS", "CALL_SESSION_PROGRESS":
		return callStageProgress
	case "CALL_RINGING", "CALL_ALERTING":
		return callStageRinging
	case "CALL_ESTABLISHED", "CALL_ANSWERED":
		return callStageConnected
	default:
		return 0
	}
}

func (c *Client) evictOldestCallLocked() {
	var oldestID string
	var oldestSequence uint64
	for id, state := range c.callStates {
		if oldestID == "" || state.sequence < oldestSequence {
			oldestID, oldestSequence = id, state.sequence
		}
	}
	if oldestID != "" {
		delete(c.callStates, oldestID)
	}
}

func classifyCallClose(param string, connected bool) (control.CallState, string) {
	value := strings.ToLower(strings.TrimSpace(param))
	switch {
	case value == "connection reset by user":
		if connected {
			return control.CallStateCompleted, "local_hangup"
		}
		return control.CallStateCanceled, "local_hangup"
	case strings.Contains(value, "connection reset by peer"):
		return control.CallStateFailed, "failed"
	case hasWord(value, "busy") || hasWord(value, "486") || hasWord(value, "600") || hasWord(value, "cause=17"):
		return control.CallStateBusy, "busy"
	case containsAny(value, "no answer", "no-answer", "no_answer", "noanswer", "timed out") || hasWord(value, "timeout") || hasWord(value, "408") || hasWord(value, "cause=18") || hasWord(value, "cause=19"):
		return control.CallStateNoAnswer, "no_answer"
	case hasWord(value, "cancel") || hasWord(value, "canceled") || hasWord(value, "cancelled") || hasWord(value, "487") || containsAny(value, "request terminated"):
		return control.CallStateCanceled, "canceled"
	case hasWord(value, "error") || hasWord(value, "failed") || hasWord(value, "failure") || hasWord(value, "provider") || hasWord(value, "network") || containsAny(value, "service unavailable") || hasWord(value, "500") || hasWord(value, "502") || hasWord(value, "503") || hasWord(value, "504"):
		return control.CallStateFailed, "failed"
	case containsAny(value, "normal clearing", "normal_clearing") || hasWord(value, "cause=16"):
		if connected {
			return control.CallStateCompleted, "normal"
		}
		return control.CallStateFailed, "unknown"
	default:
		if connected {
			return control.CallStateCompleted, "normal"
		}
		return control.CallStateFailed, "unknown"
	}
}

func containsAny(value string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func readNetstring(reader *bufio.Reader, maxSize int) ([]byte, error) {
	var size uint64
	headerBytes := 0
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		headerBytes++
		if headerBytes > 11 {
			return nil, ErrInvalidFrame
		}
		if b == ':' {
			if headerBytes == 1 {
				return nil, ErrInvalidFrame
			}
			break
		}
		if b < '0' || b > '9' || (headerBytes == 2 && size == 0) {
			return nil, ErrInvalidFrame
		}
		size = size*10 + uint64(b-'0')
		if size > uint64(maxSize) {
			return nil, ErrFrameTooLarge
		}
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	terminator, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if terminator != ',' {
		return nil, ErrInvalidFrame
	}
	return payload, nil
}

func writeNetstring(writer io.Writer, payload []byte) (int, error) {
	frame := []byte(strconv.Itoa(len(payload)) + ":")
	frame = append(frame, payload...)
	frame = append(frame, ',')
	written := 0
	for len(frame) > 0 {
		n, err := writer.Write(frame)
		if n > 0 {
			written += n
			frame = frame[n:]
		}
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func growBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}
	return current * 2
}

var _ control.Provider = (*Client)(nil)

// Baresip v1.1.0 sends AUDIO_ERROR as errno,message. Only fixed errno classes
// leave the controller; arbitrary device strings (paths, provider data) do not.
func safeAudioErrorClass(value string) string {
	code, _, _ := strings.Cut(value, ",")
	switch strings.TrimSpace(code) {
	case "90":
		return "audio_buffer_limit"
	case "71":
		return "frame_protocol"
	case "104", "32":
		return "peer_closed"
	case "5", "9", "110":
		return "socket_io"
	default:
		return "audio_device"
	}
}
