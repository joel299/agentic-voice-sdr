package baresipctrl

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

// TestLocalBaresipSmoke is an opt-in, no-call integration check. Set
// BARESIP_CTRL_TCP_ADDR to a running loopback-only Baresip ctrl_tcp listener.
func TestLocalBaresipSmoke(t *testing.T) {
	address := os.Getenv("BARESIP_CTRL_TCP_ADDR")
	if address == "" {
		t.Skip("set BARESip_CTRL_TCP_ADDR to run the local Baresip smoke check")
	}
	client, err := New(Options{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	status, err := client.RegistrationStatus(ctx)
	if err != nil {
		t.Fatalf("registration status query failed: %v", err)
	}
	if status.State != control.RegistrationRegistered {
		t.Fatalf("registration is not proven active (state=%s; response length=%d)", status.State, len(status.Detail))
	}
	inventory, err := client.ListCalls(ctx)
	if err != nil {
		t.Fatalf("list calls query failed: %v", err)
	}
	calls, err := ParseActiveCalls(inventory.Data)
	if err != nil {
		t.Fatalf("list calls response was not recognized (response length=%d)", len(inventory.Data))
	}
	if len(calls) != 0 {
		t.Fatalf("local no-call smoke requires no active calls (count=%d)", len(calls))
	}
}
