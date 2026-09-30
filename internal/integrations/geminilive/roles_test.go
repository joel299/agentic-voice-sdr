package geminilive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

type roleMessage struct {
	connection int
	payload    map[string]json.RawMessage
}

type rolePeer struct {
	id   int
	conn *websocket.Conn
}

type roleTestServer struct {
	server   *httptest.Server
	peers    chan rolePeer
	messages chan roleMessage
	setups   chan roleMessage
	nextID   atomic.Int32
}

func newRoleTestServer(t *testing.T) *roleTestServer {
	t.Helper()
	fake := &roleTestServer{peers: make(chan rolePeer, 8), messages: make(chan roleMessage, 32), setups: make(chan roleMessage, 8)}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		id := int(fake.nextID.Add(1))
		_, raw, err := conn.Read(r.Context())
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, "")
			return
		}
		var setup map[string]json.RawMessage
		if err := json.Unmarshal(raw, &setup); err != nil {
			_ = conn.Close(websocket.StatusInternalError, "")
			return
		}
		fake.setups <- roleMessage{connection: id, payload: setup}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"setupComplete":{}}`)); err != nil {
			_ = conn.Close(websocket.StatusInternalError, "")
			return
		}
		fake.peers <- rolePeer{id: id, conn: conn}
		for {
			typ, data, readErr := conn.Read(r.Context())
			if readErr != nil {
				return
			}
			if typ != websocket.MessageText && typ != websocket.MessageBinary {
				continue
			}
			var payload map[string]json.RawMessage
			if json.Unmarshal(data, &payload) == nil {
				fake.messages <- roleMessage{connection: id, payload: payload}
			}
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (s *roleTestServer) endpoint() string { return "ws" + strings.TrimPrefix(s.server.URL, "http") }

func (s *roleTestServer) connectInput(t *testing.T) InputTranscriberSession {
	t.Helper()
	session, err := ConnectInputTranscriber(context.Background(), Config{APIKey: "synthetic-key", Endpoint: s.endpoint(), Model: "injected-live-model", Tools: []ToolDefinition{{FunctionDeclarations: []FunctionDeclaration{{Name: "must-not-be-enabled"}}}}})
	if err != nil {
		t.Fatalf("connect input transcriber: %v", err)
	}
	return session
}

func (s *roleTestServer) connectResponse(t *testing.T) ControlledResponseSession {
	t.Helper()
	session, err := ConnectControlledResponse(context.Background(), Config{APIKey: "synthetic-key", Endpoint: s.endpoint(), Model: "injected-live-model", Tools: []ToolDefinition{{FunctionDeclarations: []FunctionDeclaration{{Name: "must-not-be-enabled"}}}}})
	if err != nil {
		t.Fatalf("connect controlled response: %v", err)
	}
	return session
}

func nextRolePeer(t *testing.T, ch <-chan rolePeer) rolePeer {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for synthetic provider session")
		return rolePeer{}
	}
}

func nextRoleMessage(t *testing.T, ch <-chan roleMessage) roleMessage {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for synthetic provider traffic")
		return roleMessage{}
	}
}

func rolePeers(t *testing.T, fake *roleTestServer, count int) (map[int]rolePeer, map[int]map[string]json.RawMessage) {
	t.Helper()
	setups := make(map[int]map[string]json.RawMessage, count)
	for range count {
		setup := nextRoleMessage(t, fake.setups)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(setup.payload["setup"], &fields); err != nil {
			t.Fatalf("decode setup for connection %d: %v", setup.connection, err)
		}
		setups[setup.connection] = fields
	}
	peers := make(map[int]rolePeer, count)
	for range count {
		peer := nextRolePeer(t, fake.peers)
		peers[peer.id] = peer
	}
	return peers, setups
}

func pickRolePeers(t *testing.T, peers map[int]rolePeer, setups map[int]map[string]json.RawMessage) (rolePeer, rolePeer) {
	t.Helper()
	var input, response rolePeer
	for id, fields := range setups {
		if _, ok := fields["inputAudioTranscription"]; ok {
			input = peers[id]
		}
		if _, ok := fields["outputAudioTranscription"]; ok {
			response = peers[id]
		}
	}
	if input.conn == nil || response.conn == nil {
		t.Fatal("could not identify separate transcription and response WebSockets from setup configuration")
	}
	return input, response
}

func sendProviderJSON(t *testing.T, peer rolePeer, raw string) {
	t.Helper()
	if err := peer.conn.Write(context.Background(), websocket.MessageText, []byte(raw)); err != nil {
		t.Fatalf("write synthetic provider message: %v", err)
	}
}

func TestSessionRoleCapabilitiesAreDisjoint(t *testing.T) {
	var _ InputTranscriberSession = (*inputTranscriber)(nil)
	var _ ControlledResponseSession = (*controlledResponder)(nil)
	inputType := reflect.TypeOf((*InputTranscriberSession)(nil)).Elem()
	responseType := reflect.TypeOf((*ControlledResponseSession)(nil)).Elem()
	if _, ok := inputType.MethodByName("SendTurnDirective"); ok {
		t.Fatal("input transcription capability must not send controlled directives")
	}
	if _, ok := responseType.MethodByName("SendAudio"); ok {
		t.Fatal("controlled response capability must not expose SendAudio")
	}
}

func TestRawAudioAndDirectiveRouteToSeparateProviderSessions(t *testing.T) {
	fake := newRoleTestServer(t)
	transcriber := fake.connectInput(t)
	response := fake.connectResponse(t)
	defer transcriber.Close()
	defer response.Close()
	peers, setups := rolePeers(t, fake, 2)
	inputPeer, responsePeer := pickRolePeers(t, peers, setups)
	if inputPeer.id == responsePeer.id {
		t.Fatal("input and response roles must use separate provider connections")
	}
	for _, peer := range []rolePeer{inputPeer, responsePeer} {
		fields := setups[peer.id]
		if _, hasTools := fields["tools"]; hasTools {
			t.Fatal("Gemini direct tools must be disabled on both session roles")
		}
		var model string
		_ = json.Unmarshal(fields["model"], &model)
		if model != "models/injected-live-model" {
			t.Fatalf("configured model was not preserved: %q", model)
		}
		var generation struct {
			ResponseModalities []string `json:"responseModalities"`
		}
		if err := json.Unmarshal(fields["generationConfig"], &generation); err != nil {
			t.Fatalf("decode modalities for connection %d: %v", peer.id, err)
		}
		want := "AUDIO"
		if peer.id == inputPeer.id {
			want = "TEXT"
		}
		if len(generation.ResponseModalities) != 1 || generation.ResponseModalities[0] != want {
			t.Fatalf("connection %d modalities = %v, want [%s]", peer.id, generation.ResponseModalities, want)
		}
	}

	phonePCM := []byte{0x11, 0x22, 0x33}
	if err := transcriber.SendAudio(context.Background(), phonePCM); err != nil {
		t.Fatal(err)
	}
	if err := response.SendControlledTurn(context.Background(), "final lead words", validAskDirective()); err != nil {
		t.Fatal(err)
	}
	var inputMessage roleMessage
	var responseMessages []roleMessage
	for range 3 {
		message := nextRoleMessage(t, fake.messages)
		if message.connection == inputPeer.id {
			inputMessage = message
		} else {
			responseMessages = append(responseMessages, message)
		}
	}
	realtimeRaw, found := inputMessage.payload["realtimeInput"]
	if !found {
		t.Fatal("raw phone PCM did not reach the transcription session")
	}
	var realtime struct {
		Audio struct {
			Data string `json:"data"`
		} `json:"audio"`
	}
	if err := json.Unmarshal(realtimeRaw, &realtime); err != nil || realtime.Audio.Data != base64.StdEncoding.EncodeToString(phonePCM) {
		t.Fatal("transcription provider did not receive the exact phone PCM payload")
	}
	if len(responseMessages) != 2 {
		t.Fatalf("response session got %d messages, want lead context + directive", len(responseMessages))
	}
	for _, message := range responseMessages {
		if message.connection != responsePeer.id {
			t.Fatalf("response context/directive routed to connection %d", message.connection)
		}
		if _, found := message.payload["realtimeInput"]; found {
			t.Fatal("raw phone PCM reached the controlled response session")
		}
	}
	var contextContent, directiveContent struct {
		Turns []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"turns"`
		TurnComplete bool `json:"turnComplete"`
	}
	if err := json.Unmarshal(responseMessages[0].payload["clientContent"], &contextContent); err != nil {
		t.Fatal(err)
	}
	if contextContent.TurnComplete || len(contextContent.Turns) != 1 || contextContent.Turns[0].Parts[0].Text != "final lead words" {
		t.Fatalf("final transcript was not context-only on response connection: %+v", contextContent)
	}
	if err := json.Unmarshal(responseMessages[1].payload["clientContent"], &directiveContent); err != nil {
		t.Fatal(err)
	}
	if !directiveContent.TurnComplete || !strings.Contains(directiveContent.Turns[0].Parts[0].Text, "bounded turn outcome") {
		t.Fatalf("directive was not the completing response context: %+v", directiveContent)
	}
}

func TestRoleDefaultsAndExplicitModalities(t *testing.T) {
	input := Config{}.normalized(roleInputTranscription)
	response := Config{}.normalized(roleControlledResponse)
	if len(input.ResponseModalities) != 1 || input.ResponseModalities[0] != "TEXT" {
		t.Fatalf("input transcriber default modalities = %v", input.ResponseModalities)
	}
	if len(response.ResponseModalities) != 1 || response.ResponseModalities[0] != "AUDIO" {
		t.Fatalf("controlled response default modalities = %v", response.ResponseModalities)
	}
	explicit := Config{ResponseModalities: []string{"TEXT", "AUDIO"}}
	gotInput := explicit.normalized(roleInputTranscription)
	gotResponse := explicit.normalized(roleControlledResponse)
	if strings.Join(gotInput.ResponseModalities, ",") != "TEXT,AUDIO" || strings.Join(gotResponse.ResponseModalities, ",") != "TEXT,AUDIO" {
		t.Fatal("explicit caller modalities were not preserved")
	}
}

func TestTranscriberDropsEveryModelResponseAndPreservesInterimFinal(t *testing.T) {
	fake := newRoleTestServer(t)
	transcriber := fake.connectInput(t)
	transcriber = WithLeadTurnIdentity(transcriber, NewLeadTurnSequencer(41))
	defer transcriber.Close()
	peer := nextRolePeer(t, fake.peers)
	sendProviderJSON(t, peer, `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQID"}}]}}}`)
	sendProviderJSON(t, peer, `{"serverContent":{"outputTranscription":{"text":"must-not-be-response"}}}`)
	sendProviderJSON(t, peer, `{"toolCall":{"functionCalls":[{"name":"dangerous_tool","args":{}}]}}`)
	sendProviderJSON(t, peer, `{"serverContent":{"turnComplete":true}}`)
	sendProviderJSON(t, peer, `{"serverContent":{"interrupted":true}}`)
	sendProviderJSON(t, peer, `{"serverContent":{"interimInputTranscription":{"text":"partial words"}}}`)
	sendProviderJSON(t, peer, `{"serverContent":{"inputTranscription":{"text":"final words"}}}`)

	interim, err := transcriber.Receive(context.Background())
	if err != nil || interim.State != TranscriptInterim || interim.Text != "partial words" || interim.TurnID != "lead-000042" {
		t.Fatalf("interim transcript = %+v, %v", interim, err)
	}
	final, err := transcriber.Receive(context.Background())
	if err != nil || final.State != TranscriptFinal || final.Text != "final words" || final.TurnID != interim.TurnID {
		t.Fatalf("final transcript = %+v, %v", final, err)
	}
	for _, field := range []string{"Audio", "AudioMimeType", "ToolCalls", "TurnComplete"} {
		if _, ok := reflect.TypeOf(TranscriptEvent{}).FieldByName(field); ok {
			t.Fatalf("transcription boundary leaks response field %q", field)
		}
	}
}

func TestInputTurnIdentityIsCallScopedAcrossProviderReconnect(t *testing.T) {
	fake := newRoleTestServer(t)
	owner := NewLeadTurnSequencer(0)
	connect := func() (InputTranscriberSession, rolePeer) {
		session, err := ConnectInputTranscriberWithTurnSequencer(context.Background(), Config{APIKey: "synthetic-key", Endpoint: fake.endpoint(), Model: "injected-live-model"}, owner)
		if err != nil {
			t.Fatalf("connect input session: %v", err)
		}
		return session, nextRolePeer(t, fake.peers)
	}
	first, peer := connect()
	for _, raw := range []string{
		`{"eventId":"receive-1","serverContent":{"interimInputTranscription":{"text":"Sim"}}}`,
		`{"eventId":"receive-1","serverContent":{"inputTranscription":{"text":"Sim"}}}`,
	} {
		sendProviderJSON(t, peer, raw)
	}
	firstInterim, err := first.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstFinal, err := first.Receive(context.Background())
	if err != nil || firstFinal.EventID != "receive-1" || firstInterim.TurnID != "lead-000001" || firstFinal.TurnID != firstInterim.TurnID {
		t.Fatalf("first logical turn interim=%+v final=%+v err=%v", firstInterim, firstFinal, err)
	}
	// Replaying the final in the same logical turn retains its ID.
	sendProviderJSON(t, peer, `{"eventId":"receive-1","serverContent":{"inputTranscription":{"text":"Sim"}}}`)
	replay, err := first.Receive(context.Background())
	if err != nil || replay.TurnID != firstFinal.TurnID {
		t.Fatalf("replayed final=%+v err=%v", replay, err)
	}
	for _, raw := range []string{
		`{"eventId":"receive-1","serverContent":{"interimInputTranscription":{"text":"Sim"}}}`,
		`{"eventId":"receive-1","serverContent":{"inputTranscription":{"text":"Sim"}}}`,
	} {
		sendProviderJSON(t, peer, raw)
	}
	secondLogicalInterim, err := first.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondLogicalFinal, err := first.Receive(context.Background())
	if err != nil || secondLogicalFinal.EventID != "receive-1" || secondLogicalInterim.TurnID != "lead-000002" || secondLogicalFinal.TurnID != secondLogicalInterim.TurnID {
		t.Fatalf("second logical turn interim=%+v final=%+v err=%v", secondLogicalInterim, secondLogicalFinal, err)
	}
	_ = first.Close()

	second, peer := connect()
	defer second.Close()
	for _, raw := range []string{
		`{"eventId":"receive-1","serverContent":{"interimInputTranscription":{"text":"Sim"}}}`,
		`{"eventId":"receive-1","serverContent":{"inputTranscription":{"text":"Sim"}}}`,
	} {
		sendProviderJSON(t, peer, raw)
	}
	secondInterim, err := second.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondFinal, err := second.Receive(context.Background())
	if err != nil || secondFinal.EventID != "receive-1" || secondInterim.TurnID != "lead-000003" || secondFinal.TurnID != secondInterim.TurnID {
		t.Fatalf("reconnected logical turn interim=%+v final=%+v err=%v", secondInterim, secondFinal, err)
	}
}

func TestControlledResponsePreservesOnlyControlledOutputAndLifecycle(t *testing.T) {
	fake := newRoleTestServer(t)
	response := fake.connectResponse(t)
	defer response.Close()
	peer := nextRolePeer(t, fake.peers)
	for _, raw := range []string{
		`{"serverContent":{"interimInputTranscription":{"text":"drop"}}}`,
		`{"toolCall":{"functionCalls":[{"name":"never-execute"}]}}`,
		`{"serverContent":{"outputTranscription":{"text":"spoken response"}}}`,
		`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQID"}}]}}}`,
		`{"serverContent":{"turnComplete":true}}`,
		`{"serverContent":{"interrupted":true}}`,
		`{"error":{"message":"secret provider details"}}`,
	} {
		sendProviderJSON(t, peer, raw)
	}
	want := []EventKind{EventOutputTranscription, EventAudio, EventTurnComplete, EventInterrupted, EventAPIError}
	for i, kind := range want {
		event, err := response.Receive(context.Background())
		if err != nil || event.Kind != kind {
			t.Fatalf("controlled event %d = %+v, %v, want %s", i, event, err, kind)
		}
		if event.Kind == EventAPIError && (event.Error == "" || strings.Contains(event.Error, "secret")) {
			t.Fatalf("provider error not sanitized: %+v", event)
		}
	}
}

func TestSessionReceiveRejectsConcurrentReader(t *testing.T) {
	fake := newRoleTestServer(t)
	transcriber := fake.connectInput(t)
	defer transcriber.Close()
	peer := nextRolePeer(t, fake.peers)
	input := transcriber.(*inputTranscriber)
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			_, err := input.Receive(context.Background())
			results <- err
		}()
	}
	close(start)
	var rejected bool
	for range 2 {
		select {
		case err := <-results:
			if errors.Is(err, ErrConcurrentReceiveOwner) {
				rejected = true
				goto rejectedReader
			}
		case <-time.After(3 * time.Second):
			t.Fatal("receive owner did not become active")
		}
	}
rejectedReader:
	if !rejected {
		t.Fatal("concurrent receive owner was not rejected")
	}
	sendProviderJSON(t, peer, `{"serverContent":{"inputTranscription":{"text":"one owner"}}}`)
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("active receive owner failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active receive owner did not complete")
	}
}

func TestClosingOneSessionDoesNotCorruptOtherSession(t *testing.T) {
	fake := newRoleTestServer(t)
	transcriber := fake.connectInput(t)
	response := fake.connectResponse(t)
	defer response.Close()
	peers, setups := rolePeers(t, fake, 2)
	inputPeer, responsePeer := pickRolePeers(t, peers, setups)
	if err := transcriber.Close(); err != nil {
		t.Fatal(err)
	}
	if err := response.SendControlledTurn(context.Background(), "final lead words", validAskDirective()); err != nil {
		t.Fatalf("response session failed after transcriber close: %v", err)
	}
	request := nextRoleMessage(t, fake.messages)
	if request.connection != responsePeer.id {
		t.Fatalf("response request routed to connection %d, want %d", request.connection, responsePeer.id)
	}
	sendProviderJSON(t, responsePeer, `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQID"}}]}}}`)
	event, err := response.Receive(context.Background())
	if err != nil || event.Kind != EventAudio {
		t.Fatalf("response receive after unrelated close = %+v %v", event, err)
	}
	_ = inputPeer
}

func TestClosingResponseDoesNotCorruptTranscriptionSession(t *testing.T) {
	fake := newRoleTestServer(t)
	transcriber := fake.connectInput(t)
	response := fake.connectResponse(t)
	defer transcriber.Close()
	peers, setups := rolePeers(t, fake, 2)
	inputPeer, responsePeer := pickRolePeers(t, peers, setups)
	if err := response.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transcriber.SendAudio(context.Background(), []byte{9, 8}); err != nil {
		t.Fatalf("transcription session failed after response close: %v", err)
	}
	inputMessage := nextRoleMessage(t, fake.messages)
	if inputMessage.connection != inputPeer.id {
		t.Fatalf("transcription audio routed to connection %d, want %d", inputMessage.connection, inputPeer.id)
	}
	sendProviderJSON(t, inputPeer, `{"serverContent":{"interimInputTranscription":{"text":"still open"}}}`)
	transcript, err := transcriber.Receive(context.Background())
	if err != nil || transcript.Text != "still open" {
		t.Fatalf("transcriber failed after response close: %+v %v", transcript, err)
	}
	_ = responsePeer
}

func TestTranscriberProviderErrorsAreSessionSpecificAndSanitized(t *testing.T) {
	fake := newRoleTestServer(t)
	transcriber := fake.connectInput(t)
	defer transcriber.Close()
	peer := nextRolePeer(t, fake.peers)
	sendProviderJSON(t, peer, `{"error":{"message":"synthetic-key raw transcript private"}}`)
	_, err := transcriber.Receive(context.Background())
	if !errors.Is(err, ErrTranscriptionAPI) || strings.Contains(err.Error(), "synthetic-key") || strings.Contains(err.Error(), "raw transcript") || strings.Contains(err.Error(), "private") {
		t.Fatalf("transcription provider error leaked details: %v", err)
	}
}

func TestControlledResponseMapsRemoteCloseToClosedEvent(t *testing.T) {
	fake := newRoleTestServer(t)
	response := fake.connectResponse(t)
	defer response.Close()
	peer := nextRolePeer(t, fake.peers)
	go func() { _ = peer.conn.Close(websocket.StatusNormalClosure, "synthetic close") }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	event, err := response.Receive(ctx)
	if err != nil || event.Kind != EventClosed {
		t.Fatalf("remote close = %+v, %v", event, err)
	}
}

func TestControlledResponseSessionRejectsConcurrentReader(t *testing.T) {
	fake := newRoleTestServer(t)
	response := fake.connectResponse(t)
	defer response.Close()
	peer := nextRolePeer(t, fake.peers)
	concrete := response.(*controlledResponder)
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			_, err := concrete.Receive(context.Background())
			results <- err
		}()
	}
	close(start)
	rejected := false
	for range 2 {
		select {
		case err := <-results:
			if errors.Is(err, ErrConcurrentReceiveOwner) {
				rejected = true
				goto responseRejected
			}
		case <-time.After(3 * time.Second):
			t.Fatal("response receive owner did not become active")
		}
	}
responseRejected:
	if !rejected {
		t.Fatal("concurrent response receive owner was not rejected")
	}
	sendProviderJSON(t, peer, `{"serverContent":{"turnComplete":true}}`)
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("active response receive owner failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active response receive owner did not complete")
	}
}

func TestUnderlyingProviderRoleRejectsCrossCapabilities(t *testing.T) {
	fake := newRoleTestServer(t)
	transcriber := fake.connectInput(t)
	response := fake.connectResponse(t)
	defer transcriber.Close()
	defer response.Close()
	_ = nextRolePeer(t, fake.peers)
	_ = nextRolePeer(t, fake.peers)
	if err := transcriber.(*inputTranscriber).provider.SendControlledTurn(context.Background(), "lead", validAskDirective()); !errors.Is(err, ErrCapabilityNotAllowed) {
		t.Fatalf("transcription session accepted controlled directive: %v", err)
	}
	if err := response.(*controlledResponder).provider.SendAudio(context.Background(), []byte{1, 2}); !errors.Is(err, ErrCapabilityNotAllowed) {
		t.Fatalf("response session accepted phone PCM: %v", err)
	}
	select {
	case message := <-fake.messages:
		t.Fatalf("cross-capability operation wrote provider data: %+v", message)
	default:
	}
}
