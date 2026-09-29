package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCaptureFailurePreventsRegisterTrigger(t *testing.T) {
	triggerCalls := 0
	got := runRegistrationAttempt(context.Background(), registrationAttemptSteps{
		StartCapture: func(context.Context) error { return errors.New("capture unavailable") },
		Trigger:      func(context.Context) (int, string) { triggerCalls++; return 0, "" },
	})
	if triggerCalls != 0 || got.TriggerAttempted || got.Status != "LocalError" || got.ErrorClass != "wire_capture_start_failed" {
		t.Fatalf("unexpected result: %+v triggerCalls=%d", got, triggerCalls)
	}
}

func TestRegistrationAttemptRunsCaptureApplyReadbackBeforeTrigger(t *testing.T) {
	var order []string
	triggerCalls := 0
	steps := registrationAttemptSteps{
		StartCapture: func(context.Context) error { order = append(order, "capture_start"); return nil },
		Apply:        func(context.Context) error { order = append(order, "apply"); return nil },
		Readback: func(context.Context) (bool, string, error) {
			order = append(order, "readback")
			return true, "Rejected", nil
		},
		MarkWire: func() int { order = append(order, "wire_mark"); return 4 },
		Trigger:  func(context.Context) (int, string) { order = append(order, "trigger"); triggerCalls++; return 0, "" },
		Wait: func(_ context.Context, pre string, triggered bool) string {
			order = append(order, "wait")
			if pre != "Rejected" || !triggered {
				t.Fatalf("pre-state=%q triggered=%v", pre, triggered)
			}
			return "Rejected"
		},
		StopCapture: func() { order = append(order, "capture_stop") },
		ReadWire: func(mark int) sipWireEvidence {
			order = append(order, "wire_read")
			if mark != 4 {
				t.Fatalf("wire mark=%d", mark)
			}
			return sipWireEvidence{Initial: true, FinalResponse: "403", FinalReason: "Forbidden"}
		},
	}
	got := runRegistrationAttempt(context.Background(), steps)
	want := []string{"capture_start", "apply", "readback", "wire_mark", "trigger", "wait", "capture_stop", "wire_read"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order=%v, want %v", order, want)
	}
	if triggerCalls != 1 || !got.Triggered || got.PreState != "Rejected" || got.Status != "Rejected" || !got.WireActivity {
		t.Fatalf("unexpected result: %+v triggerCalls=%d", got, triggerCalls)
	}
}

func TestCappedBufferReadAfterMarkExcludesEarlierPackets(t *testing.T) {
	buffer := &cappedBuffer{}
	_, _ = buffer.Write([]byte("old REGISTER"))
	mark := buffer.Mark()
	_, _ = buffer.Write([]byte("new REGISTER"))
	if got := buffer.StringFrom(mark); got != "new REGISTER" {
		t.Fatalf("wire slice=%q", got)
	}
}

func TestPreexistingRejectedDoesNotEndRegistrationPoll(t *testing.T) {
	if shouldStopRegistrationPoll("Rejected", "Rejected") {
		t.Fatal("unchanged pre-existing Rejected state ended the fresh-attempt poll")
	}
	if !shouldStopRegistrationPoll("Unregistered", "Rejected") {
		t.Fatal("new Rejected state did not end poll")
	}
	if !shouldStopRegistrationPoll("Rejected", "Registered") {
		t.Fatal("Registered state did not end poll")
	}
}

func TestRegistrationAttemptNoWireActivityIsLocalDespitePreexistingRejected(t *testing.T) {
	steps := registrationAttemptSteps{
		StartCapture: func(context.Context) error { return nil },
		Apply:        func(context.Context) error { return nil },
		Readback:     func(context.Context) (bool, string, error) { return true, "Rejected", nil },
		MarkWire:     func() int { return 0 },
		Trigger:      func(context.Context) (int, string) { return 0, "" },
		Wait: func(_ context.Context, pre string, triggered bool) string {
			if pre != "Rejected" || !triggered {
				t.Fatalf("pre=%s triggered=%t", pre, triggered)
			}
			return "Rejected"
		},
		StopCapture: func() {},
		ReadWire:    func(int) sipWireEvidence { return sipWireEvidence{} },
	}
	got := runRegistrationAttempt(context.Background(), steps)
	if got.Status != "NoWireActivity" || got.ErrorClass != "registration_trigger_no_wire" {
		t.Fatalf("got %+v", got)
	}
}

func TestRegistrationAttemptDoesNotConsumeStaleRejectedWithoutWire(t *testing.T) {
	got := classifyRegistrationAttempt("Rejected", "Rejected", "", sipWireEvidence{})
	if got.Status != "NoWireActivity" || got.ErrorClass != "registration_trigger_no_wire" {
		t.Fatalf("got %+v", got)
	}
}

func TestRegistrationAttemptTriggerFailureIsLocal(t *testing.T) {
	got := classifyRegistrationAttempt("Unregistered", "Unregistered", "asterisk_cli_failed", sipWireEvidence{})
	if got.Status != "TriggerFailed" || got.ErrorClass != "asterisk_cli_failed" {
		t.Fatalf("got %+v", got)
	}
}

func TestRegistrationAttemptWireResponsesDetermineProviderStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		post string
		wire sipWireEvidence
		want string
	}{
		{"403", "Rejected", sipWireEvidence{Initial: true, FinalResponse: "403"}, "Rejected"},
		{"200", "Registered", sipWireEvidence{Initial: true, FinalResponse: "200"}, "Registered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyRegistrationAttempt("Unregistered", tc.post, "", tc.wire)
			if got.Status != tc.want {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestExplicitRegisterCommandIssuedExactlyOnce(t *testing.T) {
	calls := 0
	code, class := sendAsteriskRegister(context.Background(), func(_ context.Context, command string) (string, int, error) {
		calls++
		if command != "pjsip send register trunk-falepaco-reg" {
			t.Fatalf("command=%q", command)
		}
		return "", 0, nil
	})
	if calls != 1 || code != 0 || class != "" {
		t.Fatalf("calls=%d code=%d class=%q", calls, code, class)
	}
}

func TestExplicitRegisterCommandFailureIsClassifiedLocally(t *testing.T) {
	code, class := sendAsteriskRegister(context.Background(), func(_ context.Context, command string) (string, int, error) {
		if command != "pjsip send register trunk-falepaco-reg" {
			t.Fatalf("command=%q", command)
		}
		return "", 23, errors.New("transport failed")
	})
	if code != 23 || class != "asterisk_cli_failed" {
		t.Fatalf("code=%d class=%q", code, class)
	}
}

func TestCallRequiresCurrentRegisteredState(t *testing.T) {
	if registrationStateFromOutput("Status: Unregistered") != "Unregistered" || registrationStateFromOutput("Status: Registered") != "Registered" {
		t.Fatal("registration output parser did not distinguish Registered from Unregistered")
	}
	if callRegistrationReady(registrationStateFromOutput("Status: Unregistered")) || callRegistrationReady("Rejected") || callRegistrationReady("NoWireActivity") {
		t.Fatal("call gate accepted a non-registered state")
	}
	if !callRegistrationReady(registrationStateFromOutput("Status: Registered")) {
		t.Fatal("call gate rejected Registered")
	}
}

func TestParseSIPWireCaptureExtractsSanitizedDigestEvidence(t *testing.T) {
	capture := `REGISTER sip:host SIP/2.0
SIP/2.0 401 Unauthorized
WWW-Authenticate: Digest realm="testrealm@host.com", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", qop="auth", algorithm=MD5
REGISTER sip:host SIP/2.0
Authorization: Digest username="Mufasa", realm="testrealm@host.com", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", uri="/dir/index.html", qop=auth, nc=00000001, cnonce="0a4f113b", response="59d17b90f0e821045ecceb843e5b38c4"
SIP/2.0 200 OK
Server: provider-test
`
	evidence := parseSIPWireCapture(capture, "Mufasa", "Circle Of Life")
	if !evidence.Initial || evidence.FirstResponse != "401" || !evidence.Challenge || evidence.ChallengeType != "401" || evidence.Realm != "testrealm@host.com" {
		t.Fatalf("challenge evidence incomplete: %+v", evidence)
	}
	if !evidence.Authenticated || evidence.AuthUsername != "Mufasa" || evidence.AuthRealm != "testrealm@host.com" || evidence.AuthURI != "/dir/index.html" || evidence.DigestMatches == nil || !*evidence.DigestMatches {
		t.Fatalf("digest evidence incorrect: %+v", evidence)
	}
	if evidence.FinalResponse != "200" || evidence.FinalReason != "OK" || evidence.Server != "provider-test" {
		t.Fatalf("final response evidence incomplete: %+v", evidence)
	}
}

func TestParseSIPWireCaptureDoesNotClaimUnseenTraffic(t *testing.T) {
	evidence := parseSIPWireCapture("tcpdump: listening on any", "100", "secret")
	if evidence.Initial || evidence.Challenge || evidence.Authenticated || evidence.FinalResponse != "" {
		t.Fatalf("capture without SIP packets must not imply REGISTER evidence: %+v", evidence)
	}
}
