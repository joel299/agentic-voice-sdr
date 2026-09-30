package baresipctrl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

type testRequest struct {
	Command string `json:"command"`
	Params  string `json:"params"`
	Token   string `json:"token"`
}

func TestClientCorrelatesResponseAndNormalizesEvents(t *testing.T) {
	listener := testListener(t)
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		request, err := readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if request.Command != "reginfo" || request.Token == "" {
			serverErr <- fmt.Errorf("unexpected command or empty token")
			return
		}
		if err := writeTestMessage(conn, `{"event":true,"class":"call","type":"CALL_PROGRESS","id":"call-7","peeruri":"sip:peer@example.test"}`); err != nil {
			serverErr <- err
			return
		}
		serverErr <- writeTestMessage(conn, fmt.Sprintf(`{"response":true,"ok":true,"data":"Registered","token":%q}`, request.Token))
	}()

	client := newTestClient(t, listener.Addr().String(), 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Do(ctx, "reginfo", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Data != "Registered" || result.Token == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	select {
	case event := <-client.Events():
		if event.CallID != "call-7" || event.PeerURI != "sip:peer@example.test" || event.State != control.CallStateProgress {
			t.Fatalf("unexpected normalized event: %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestStalledEventConsumerDoesNotBlockCommandResponses(t *testing.T) {
	listener := testListener(t)
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		for i := 0; i < 32; i++ {
			message := fmt.Sprintf(`{"event":true,"class":"call","type":"CALL_PROGRESS","id":"call-%d"}`, i)
			if err := writeTestMessage(conn, message); err != nil {
				serverErr <- err
				return
			}
		}
		request, err := readTestRequest(conn)
		if err == nil && request.Command != "listcalls" {
			err = fmt.Errorf("command = %q, want listcalls", request.Command)
		}
		if err == nil {
			err = writeTestMessage(conn, fmt.Sprintf(`{"response":true,"ok":true,"data":"ok","token":%q}`, request.Token))
		}
		serverErr <- err
	}()

	client, err := New(Options{Address: listener.Addr().String(), EventBuffer: 1, MaxPending: 1, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Deliberately leave Events() undrained while the server fills the buffer.
	result, err := client.Do(ctx, "listcalls", "")
	if err != nil {
		t.Fatalf("command response was blocked by event backpressure: %v", err)
	}
	if !result.OK || result.Data != "ok" {
		t.Fatalf("unexpected command response: %#v", result)
	}
	if got := len(client.Events()); got > cap(client.Events()) {
		t.Fatalf("event buffer grew beyond its bound: len=%d cap=%d", got, cap(client.Events()))
	}
	if got := client.DroppedEventCount(); got == 0 {
		t.Fatal("expected overflow policy to drop events for the stalled consumer")
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}

	closed := make(chan struct{})
	go func() { _ = client.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked while event buffer was full")
	}
}

func TestCancelWithSaturatedEventBuffer(t *testing.T) {
	listener := testListener(t)
	defer listener.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			for i := 0; i < 8; i++ {
				if writeTestMessage(conn, `{"event":true,"class":"call","type":"CALL_PROGRESS","id":"call-cancel"}`) != nil {
					return
				}
			}
			_, _ = io.Copy(io.Discard, conn)
		}
	}()

	client, err := New(Options{Address: listener.Addr().String(), EventBuffer: 1, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for client.DroppedEventCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if client.DroppedEventCount() == 0 {
		t.Fatal("server events did not saturate the event channel")
	}
	cancel()
	select {
	case <-client.runDone:
	case <-time.After(time.Second):
		t.Fatal("context cancellation blocked while event buffer was full")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	<-serverDone
}

func TestCallClosedTerminalStatesByCallID(t *testing.T) {
	tests := []struct {
		name      string
		sequence  []wireMessage
		want      control.CallState
		wantParam string
	}{
		{
			name: "connected then normal close completes",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "connected"},
				{Class: "call", Type: "CALL_PROGRESS", CallID: "connected"},
				{Class: "call", Type: "CALL_ESTABLISHED", CallID: "connected"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "connected", Param: "Q.850 cause=16 NORMAL_CLEARING"},
			},
			want: control.CallStateCompleted, wantParam: "normal",
		},
		{
			name: "busy before connect",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "busy"},
				{Class: "call", Type: "CALL_RINGING", CallID: "busy"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "busy", Param: "486 Busy Here"},
			},
			want: control.CallStateBusy, wantParam: "busy",
		},
		{
			name: "Q.850 cause 17 is busy before connect",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "cause-17"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "cause-17", Param: "cause=17"},
			},
			want: control.CallStateBusy, wantParam: "busy",
		},
		{
			name: "no answer timeout",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "no-answer"},
				{Class: "call", Type: "CALL_RINGING", CallID: "no-answer"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "no-answer", Param: "no answer timeout"},
			},
			want: control.CallStateNoAnswer, wantParam: "no_answer",
		},
		{
			name: "provider failure",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "failure"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "failure", Param: "provider error 503"},
			},
			want: control.CallStateFailed, wantParam: "failed",
		},
		{
			name: "unknown preconnect reason is not success",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "unknown"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "unknown", Param: "unclassified"},
			},
			want: control.CallStateFailed, wantParam: "unknown",
		},
		{
			name: "explicit cancellation",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "canceled"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "canceled", Param: "487 Request Terminated"},
			},
			want: control.CallStateCanceled, wantParam: "canceled",
		},
		{
			name: "Baresip local hangup before connect is canceled",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "local-hangup-before"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "local-hangup-before", Param: "Connection reset by user"},
			},
			want: control.CallStateCanceled, wantParam: "local_hangup",
		},
		{
			name: "Baresip local hangup after connect completes",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "local-hangup-after"},
				{Class: "call", Type: "CALL_ESTABLISHED", CallID: "local-hangup-after"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "local-hangup-after", Param: "Connection reset by user"},
			},
			want: control.CallStateCompleted, wantParam: "local_hangup",
		},
		{
			name: "peer reset is a failure, not a local cancellation",
			sequence: []wireMessage{
				{Class: "call", Type: "CALL_OUTGOING", CallID: "peer-reset"},
				{Class: "call", Type: "CALL_CLOSED", CallID: "peer-reset", Param: "Connection reset by peer"},
			},
			want: control.CallStateFailed, wantParam: "failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newCallLifecycleClient(128)
			var terminal control.Event
			for _, message := range test.sequence {
				terminal = client.normalizeLifecycleEvent(normalizeEvent(message))
			}
			if terminal.State != test.want || terminal.Param != test.wantParam {
				t.Fatalf("terminal state/param = %q/%q, want %q/%q", terminal.State, terminal.Param, test.want, test.wantParam)
			}
			if len(client.callStates) != 0 {
				t.Fatalf("terminal call state was retained: %#v", client.callStates)
			}
		})
	}
}

func TestInterleavedCallLifecycleStateIsIsolated(t *testing.T) {
	client := newCallLifecycleClient(4)
	feed := func(id, eventType, param string) control.Event {
		return client.normalizeLifecycleEvent(normalizeEvent(wireMessage{Class: "call", Type: eventType, CallID: id, Param: param}))
	}
	feed("call-a", "CALL_OUTGOING", "")
	feed("call-b", "CALL_OUTGOING", "")
	feed("call-a", "CALL_ESTABLISHED", "")
	callB := feed("call-b", "CALL_CLOSED", "unclassified")
	callA := feed("call-a", "CALL_CLOSED", "Q.850 cause=16 NORMAL_CLEARING")
	if callB.State != control.CallStateFailed {
		t.Fatalf("call-b terminal state = %q, want FAILED", callB.State)
	}
	if callA.State != control.CallStateCompleted {
		t.Fatalf("call-a terminal state = %q, want COMPLETED", callA.State)
	}
	if len(client.callStates) != 0 {
		t.Fatalf("terminal call states were retained: %#v", client.callStates)
	}
}

func TestCallLifecycleTracksEachObservedStage(t *testing.T) {
	client := newCallLifecycleClient(4)
	for _, eventType := range []string{"CALL_OUTGOING", "CALL_PROGRESS", "CALL_RINGING", "CALL_ESTABLISHED"} {
		client.normalizeLifecycleEvent(normalizeEvent(wireMessage{Class: "call", Type: eventType, CallID: "call-stages"}))
	}
	state, ok := client.callStates["call-stages"]
	want := callStageOutgoing | callStageProgress | callStageRinging | callStageConnected
	if !ok || state.stages != want {
		t.Fatalf("observed stages = %04b, found=%t; want %04b", state.stages, ok, want)
	}
}

func TestCallLifecycleStateIsBounded(t *testing.T) {
	client := newCallLifecycleClient(2)
	for _, id := range []string{"a", "b", "c"} {
		client.normalizeLifecycleEvent(normalizeEvent(wireMessage{Class: "call", Type: "CALL_OUTGOING", CallID: id}))
	}
	if got := len(client.callStates); got != 2 {
		t.Fatalf("call lifecycle states = %d, want bounded at 2", got)
	}
}

func newCallLifecycleClient(maxCallStates int) *Client {
	return &Client{maxCallStates: maxCallStates, callStates: make(map[string]callLifecycle)}
}

func TestProviderCommandsAndRegistrationStatus(t *testing.T) {
	listener := testListener(t)
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for _, command := range []string{"reginfo", "dial", "hangup", "listcalls"} {
			payload, err := readNetstring(reader, 1<<20)
			if err != nil {
				serverErr <- err
				return
			}
			var request testRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				serverErr <- err
				return
			}
			if request.Command != command {
				serverErr <- fmt.Errorf("command = %q, want %q", request.Command, command)
				return
			}
			data := "ok"
			if command == "reginfo" {
				data = "Registered: 200 OK [1 binding]"
			}
			response, _ := json.Marshal(map[string]any{"response": true, "ok": true, "data": data, "token": request.Token})
			if err := writeTestMessage(conn, string(response)); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	client := newTestClient(t, listener.Addr().String(), 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	status, err := client.RegistrationStatus(ctx)
	if err != nil || status.State != control.RegistrationRegistered {
		t.Fatalf("registration status = %#v, error = %v", status, err)
	}
	if _, err := client.Dial(ctx, "sip:peer@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListCalls(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationStatusUsesBaresipRegisterEvent(t *testing.T) {
	listener := testListener(t)
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		request, err := readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if err = writeTestMessage(conn, `{"event":true,"class":"register","type":"REGISTER_OK","param":"200 OK"}`); err != nil {
			serverErr <- err
			return
		}
		serverErr <- writeTestMessage(conn, fmt.Sprintf(`{"response":true,"ok":true,"data":"","token":%q}`, request.Token))
	}()

	client := newTestClient(t, listener.Addr().String(), 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	status, err := client.RegistrationStatus(ctx)
	if err != nil || status.State != control.RegistrationRegistered {
		t.Fatalf("registration status = %#v, error = %v", status, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestReginfoParsesBaresipANSIUserAgentStatus(t *testing.T) {
	detail := "--- User Agents (1) ---\n0 - sip:100@example.test \x1b[32mOK \x1b[;m Expires 600s"
	if !reginfoHasActiveUserAgent(ansiEscape.ReplaceAllString(detail, "")) {
		t.Fatal("expected ANSI-colored active Baresip user agent to be recognized")
	}
	if count, ok := reginfoUserAgentCount("--- User Agents (0) ---"); !ok || count != 0 {
		t.Fatalf("user agent count = %d, found=%t; want 0, true", count, ok)
	}
	if reginfoHasActiveUserAgent("--- User Agents (1) ---\n0 - sip:100@example.test Rejected Expires 0s") {
		t.Fatal("rejected Baresip user agent was reported active")
	}
}

func TestClientRejectsCommandsOutsideOutboundAllowlist(t *testing.T) {
	client, err := New(Options{Address: "127.0.0.1:4444"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Do(context.Background(), "quit", ""); !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("error = %v, want ErrUnsupportedCommand", err)
	}
}

func TestNewRejectsNonLoopbackAddress(t *testing.T) {
	if _, err := New(Options{Address: "192.0.2.10:4444"}); !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("error = %v, want ErrInvalidAddress", err)
	}
}

func TestClientPendingCommandsAreBounded(t *testing.T) {
	listener := testListener(t)
	defer listener.Close()
	firstReceived := make(chan struct{})
	releaseResponse := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		request, err := readTestRequest(conn)
		if err != nil {
			return
		}
		close(firstReceived)
		<-releaseResponse
		_ = writeTestMessage(conn, fmt.Sprintf(`{"response":true,"ok":true,"data":"ok","token":%q}`, request.Token))
	}()

	client := newTestClient(t, listener.Addr().String(), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	firstDone := make(chan error, 1)
	go func() { _, err := client.Do(ctx, "reginfo", ""); firstDone <- err }()
	select {
	case <-firstReceived:
	case <-ctx.Done():
		t.Fatal("first command was not received")
	}
	if _, err := client.Do(ctx, "listcalls", ""); !errors.Is(err, ErrPendingLimit) {
		t.Fatalf("second command error = %v, want ErrPendingLimit", err)
	}
	close(releaseResponse)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	<-serverDone
}

func TestClientCancellationDoesNotRetryCommand(t *testing.T) {
	listener := testListener(t)
	defer listener.Close()
	requests := make(chan testRequest, 2)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		request, err := readTestRequest(conn)
		if err == nil {
			requests <- request
		}
		<-time.After(250 * time.Millisecond)
	}()

	client := newTestClient(t, listener.Addr().String(), 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	commandCtx, cancelCommand := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := client.Do(commandCtx, "dial", "sip:peer@example.test"); done <- err }()
	select {
	case <-requests:
	case <-ctx.Done():
		t.Fatal("dial command was not received")
	}
	cancelCommand()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("command error = %v, want context.Canceled", err)
	}
	select {
	case duplicate := <-requests:
		t.Fatalf("command was unexpectedly replayed: %#v", duplicate)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestClientReconnectsAfterConnectionLoss(t *testing.T) {
	listener := testListener(t)
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		first, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		request, err := readTestRequest(first)
		if err != nil {
			first.Close()
			serverErr <- err
			return
		}
		if request.Command != "reginfo" {
			first.Close()
			serverErr <- fmt.Errorf("unexpected first command")
			return
		}
		_ = first.Close()

		second, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer second.Close()
		request, err = readTestRequest(second)
		if err == nil {
			err = writeTestMessage(second, fmt.Sprintf(`{"response":true,"ok":true,"data":"ok","token":%q}`, request.Token))
		}
		serverErr <- err
	}()

	client := newTestClient(t, listener.Addr().String(), 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Do(ctx, "reginfo", ""); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("first command error = %v, want ErrDisconnected", err)
	}
	if err := client.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(ctx, "reginfo", ""); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestReadNetstringRejectsOversizedFrame(t *testing.T) {
	_, err := readNetstring(bufio.NewReader(strings.NewReader("9:123456789,")), 4)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("error = %v, want ErrFrameTooLarge", err)
	}
}

func testListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func newTestClient(t *testing.T, address string, maxPending int) *Client {
	t.Helper()
	client, err := New(Options{Address: address, EventBuffer: 2, MaxPending: maxPending, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func readTestRequest(conn net.Conn) (testRequest, error) {
	payload, err := readNetstring(bufio.NewReader(conn), 1<<20)
	if err != nil {
		return testRequest{}, err
	}
	var request testRequest
	err = json.Unmarshal(payload, &request)
	return request, err
}

func writeTestMessage(conn net.Conn, payload string) error {
	frame := []byte(payload)
	_, err := fmt.Fprintf(conn, "%d:%s,", len(frame), frame)
	return err
}

var _ io.Reader = (*strings.Reader)(nil)
