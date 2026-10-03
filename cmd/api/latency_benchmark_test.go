package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
	promptpostgres "github.com/joel299/agentic-voice-sdr/internal/integrations/postgres/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/sessionprompt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Explicit provider-only opt-in. Never connects to Baresip, dials, or retries.
func TestRealNoCallLatencyBenchmark(t *testing.T) {
	out := os.Getenv("GRU152_BENCHMARK_OUTPUT")
	if out == "" {
		t.Skip("set GRU152_BENCHMARK_OUTPUT for paid no-call provider measurements")
	}
	root, _ := filepath.Abs("../..")
	if e := config.LoadLocalEnv(filepath.Join(root, ".env")); e != nil {
		t.Fatal("private environment unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	pool, e := pgxpool.New(ctx, "")
	if e != nil {
		t.Fatal("database unavailable")
	}
	defer pool.Close()
	repo, _ := promptpostgres.NewRepository(pool)
	ps, _ := agentprompt.NewPromptService(repo)
	gc := geminilive.ConfigFromEnv()
	gc.VoiceName = "Kore"
	gc.VoiceDescription = "Voz comercial brasileira, humana e consultiva. Deve transmitir clareza, proximidade e confiança sem parecer locução publicitária."
	gc.VoiceStyle = "Calmo e confiante. Ritmo moderado. Frases curtas. Tom acolhedor e profissional."
	if style := os.Getenv("GRU152_BENCHMARK_STYLE"); style != "" {
		gc.VoiceStyle = style
	}
	if v := os.Getenv("GRU152_BENCHMARK_VOICE"); v != "" {
		gc.VoiceName = v
	}
	builder, _ := sessionprompt.NewBuilder(gc, immutableGeminiCore, ps)
	gc, _, e = builder.Build(ctx)
	if e != nil {
		t.Fatal("prompt unavailable")
	}
	jc, e := openrouterjev.ConfigFromEnv()
	if e != nil {
		t.Fatal("JEV unavailable")
	}
	jc.Description = "Classificador comercial SDR responsável por selecionar a próxima ação."
	jc.DecisionGuidance = "Classifique semanticamente o último turno FINAL e selecione a próxima ação comercial."
	var network []openrouterjev.RequestTiming
	jc.ObserveRequest = func(t openrouterjev.RequestTiming) { network = append(network, t) }
	jc.CompactInstructions = os.Getenv("GRU152_BENCHMARK_COMPACT_JEV") == "true"
	j, e := openrouterjev.NewWithTimeout(jc, 1500*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	phrases := []string{"Sim, podemos conversar.", "Quero entender como funciona.", "Preciso conversar com meu sócio antes de decidir.", "Parece caro, preciso avaliar o custo.", "Pode me procurar na próxima semana."}
	results := map[string]any{"model": gc.Model, "voice": gc.VoiceName, "system_instruction_bytes": len(gc.SystemInstruction), "retry_count": 0, "real_call_count": 0}
	var jevRows []map[string]any
	for i := 0; i < 20; i++ {
		input, _ := testDecisionInput(phrases[i%5], "active")
		start := time.Now()
		v, e := j.DecideDetailed(ctx, input)
		row := map[string]any{"index": i, "ms": time.Since(start).Milliseconds(), "ok": e == nil, "intent": v.Intent}
		if e != nil {
			category := "provider_error"
			if errors.Is(e, openrouterjev.ErrTimeout) {
				category = "timeout"
			}
			row["failure_class"] = category
		}
		jevRows = append(jevRows, row)
		t.Logf("jev %d ok=%v ms=%v", i, e == nil, row["ms"])
	}
	results["jev"] = jevRows
	r, e := geminilive.ConnectControlledResponse(ctx, gc)
	if e != nil {
		t.Fatal("Gemini connection failed")
	}
	defer r.Close()
	run := func(text string, d conversation.TurnDirective) map[string]any {
		c, stop := context.WithTimeout(ctx, 35*time.Second)
		defer stop()
		start := time.Now()
		row := map[string]any{"ok": false}
		if e := r.SendControlledTurn(c, text, d); e != nil {
			return row
		}
		first := false
		bytes := 0
		for {
			v, e := r.Receive(c)
			if e != nil {
				return row
			}
			switch v.Kind {
			case geminilive.EventAudio:
				if !first {
					row["first_audio_ms"] = time.Since(start).Milliseconds()
					first = true
				}
				bytes += len(v.Audio)
			case geminilive.EventTurnComplete:
				row["ok"] = first
				row["audio_bytes"] = bytes
				row["total_ms"] = time.Since(start).Milliseconds()
				return row
			case geminilive.EventClosed, geminilive.EventAPIError:
				return row
			}
		}
	}
	var geminiRows []map[string]any
	d := conversation.TurnDirective{Kind: conversation.ActionAskQuestion, Reason: conversation.ReasonNeedsClarification}
	for i := 0; i < 20; i++ {
		row := run(phrases[i%5], d)
		geminiRows = append(geminiRows, row)
		t.Logf("gemini %d ok=%v first_ms=%v", i, row["ok"], row["first_audio_ms"])
		if row["ok"] != true {
			break
		}
	}
	results["gemini"] = geminiRows
	var full []map[string]any
	for i := 0; i < 5; i++ {
		start := time.Now()
		in, _ := testDecisionInput(phrases[i], "active")
		v, e := j.DecideDetailed(ctx, in)
		if e != nil {
			full = append(full, map[string]any{"ok": false, "stage": "jev"})
			continue
		}
		d, e := conversation.BuildTurnDirective(conversation.OrchestratorInput{DecisionInput: in, Decision: v.Decision})
		if e != nil {
			full = append(full, map[string]any{"ok": false, "stage": "directive"})
			continue
		}
		jevMS := time.Since(start).Milliseconds()
		row := run(phrases[i], d)
		row["jev_ms"] = jevMS
		if n, ok := row["first_audio_ms"].(int64); ok {
			row["final_text_to_first_audio_ms"] = n + jevMS
		}
		full = append(full, row)
		t.Logf("pipeline %d %v", i, row)
	}
	results["pipeline"] = full
	results["jev_network"] = network
	data, _ := json.MarshalIndent(results, "", "  ")
	if e := os.WriteFile(out, data, 0600); e != nil {
		t.Fatal(e)
	}
	for _, rows := range [][]map[string]any{jevRows, geminiRows, full} {
		for _, row := range rows {
			if row["ok"] != true {
				t.Error("real provider gate has unsuccessful attempts; evidence retained, no retry")
			}
		}
	}
}
