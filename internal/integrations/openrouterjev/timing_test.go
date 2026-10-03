package openrouterjev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestTimingMeasuresReuseWithoutContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(providerBody("neutral_continue")))
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	var rows []RequestTiming
	cfg.ObserveRequest = func(v RequestTiming) { rows = append(rows, v) }
	client, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e = client.DecideDetailed(context.Background(), activeInput()); e != nil {
			t.Fatal(e)
		}
	}
	if len(rows) != 2 || rows[0].RequestBytes <= 0 || rows[0].TotalMS <= 0 || !rows[1].ConnectionReused {
		t.Fatalf("missing trace: %+v", rows)
	}
	b, _ := json.Marshal(rows)
	for _, secret := range []string{testAPIKey, "lead@example.com", "LatestFinalLeadText", "http://"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("content leaked into timing")
		}
	}
}
