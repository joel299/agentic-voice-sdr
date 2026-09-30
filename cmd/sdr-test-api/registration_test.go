package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
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
		MarkWire: func() time.Time { order = append(order, "wire_mark"); return time.Unix(1, 0) },
		Trigger:  func(context.Context) (int, string) { order = append(order, "trigger"); triggerCalls++; return 0, "" },
		Wait: func(_ context.Context, pre string, triggered bool) string {
			order = append(order, "wait")
			if pre != "Rejected" || !triggered {
				t.Fatalf("pre-state=%q triggered=%v", pre, triggered)
			}
			return "Rejected"
		},
		StopCapture: func() (time.Time, error) { order = append(order, "capture_stop"); return time.Unix(2, 0), nil },
		ReadWire: func(mark time.Time) sipWireEvidence {
			order = append(order, "wire_read")
			if !mark.Equal(time.Unix(1, 0)) {
				t.Fatalf("wire mark=%v", mark)
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
		MarkWire:     func() time.Time { return time.Now() },
		Trigger:      func(context.Context) (int, string) { return 0, "" },
		Wait: func(_ context.Context, pre string, triggered bool) string {
			if pre != "Rejected" || !triggered {
				t.Fatalf("pre=%s triggered=%t", pre, triggered)
			}
			return "Rejected"
		},
		StopCapture: func() (time.Time, error) { return time.Now(), nil },
		ReadWire:    func(time.Time) sipWireEvidence { return sipWireEvidence{} },
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
