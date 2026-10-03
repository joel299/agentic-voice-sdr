package main

import (
	"context"
	"encoding/json"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRealJEVCompactAB(t *testing.T) {
	out := os.Getenv("GRU152_JEV_AB_OUTPUT")
	if out == "" {
		t.Skip("explicit paid no-call classifier A/B")
	}
	root, _ := filepath.Abs("../..")
	if e := config.LoadLocalEnv(filepath.Join(root, ".env")); e != nil {
		t.Fatal("env unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	phrases := []string{"Sim, podemos conversar.", "Quero entender como funciona.", "Preciso conversar com meu sócio antes de decidir.", "Parece caro, preciso avaliar o custo.", "Pode me procurar na próxima semana."}
	var rows []map[string]any
	for _, compact := range []bool{false, true} {
		cfg, e := openrouterjev.ConfigFromEnv()
		if e != nil {
			t.Fatal("JEV unavailable")
		}
		cfg.CompactInstructions = compact
		var timing openrouterjev.RequestTiming
		cfg.ObserveRequest = func(v openrouterjev.RequestTiming) { timing = v }
		c, _ := openrouterjev.NewWithTimeout(cfg, 1500*time.Millisecond)
		for i := 0; i < 20; i++ {
			input, _ := testDecisionInput(phrases[i%5], "active")
			v, e := c.DecideDetailed(ctx, input)
			row := map[string]any{"compact": compact, "index": i, "ok": e == nil, "intent": v.Intent, "network": timing}
			rows = append(rows, row)
			t.Logf("compact=%v index=%d ok=%v ms=%.1f reused=%v bytes=%d intent=%s", compact, i, e == nil, timing.TotalMS, timing.ConnectionReused, timing.RequestBytes, v.Intent)
		}
	}
	data, _ := json.MarshalIndent(rows, "", "  ")
	if e := os.WriteFile(out, data, 0600); e != nil {
		t.Fatal(e)
	}
}
