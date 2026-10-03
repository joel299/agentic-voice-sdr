package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
	pg "github.com/joel299/agentic-voice-sdr/internal/integrations/postgres/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/sessionprompt"
)

// Paid provider gate, opt-in only. No controller, accounts, SIP, retry, or
// persisted PCM. The first turn bypasses JEV only inside this isolated test.
func TestRealNoCallControlledResponseRegression(t *testing.T) {
	output := os.Getenv("GRU152_P0_CONTROLLED_OUTPUT")
	if output == "" {
		t.Skip("explicit real-provider no-call response gate")
	}
	root, err := filepath.Abs("../..")
	if err != nil || config.LoadLocalEnv(filepath.Join(root, ".env")) != nil {
		t.Fatal("protected environment unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		t.Fatal("prompt database unavailable")
	}
	defer pool.Close()
	repo, err := pg.NewRepository(pool)
	if err != nil {
		t.Fatal("prompt repository unavailable")
	}
	service, err := agentprompt.NewPromptService(repo)
	if err != nil {
		t.Fatal("prompt service unavailable")
	}
	cfg := geminilive.ConfigFromEnv()
	cfg.VoiceName = "Fola"
	cfg.VoiceDescription = "Voz comercial brasileira, humana e consultiva. Deve transmitir clareza, proximidade e confiança sem parecer locução publicitária."
	cfg.VoiceStyle = "PT-BR natural e consultivo. Responda em uma ou duas frases curtas, com uma pergunta por vez. <breath> indica uma respiração discreta numa pausa natural; não pronuncie a marcação nem acrescente pausas longas."
	builder, err := sessionprompt.NewBuilder(cfg, immutableGeminiCore, service)
	if err != nil {
		t.Fatal("prompt builder unavailable")
	}
	cfg, snapshot, err := builder.Build(ctx)
	if err != nil {
		t.Fatal("active prompt unavailable")
	}
	result := map[string]any{"voice": cfg.VoiceName, "prompt_version": snapshot.Version(), "real_calls": 0, "retry": 0}
	defer func() {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil || os.WriteFile(output, data, 0600) != nil {
			t.Error("sanitized metadata output unavailable")
		}
	}()
	response, err := geminilive.ConnectControlledResponse(ctx, cfg)
	result["setup"] = err == nil
	if err != nil {
		t.Fatal("Gemini setup failed")
	}
	defer response.Close()
	const lead = "Sim, podemos conversar. Quero entender como funciona."
	run := func(name string, directive conversation.TurnDirective) {
		t.Helper()
		turnCtx, stop := context.WithTimeout(ctx, 25*time.Second)
		defer stop()
		flags := map[string]bool{"send": false, "output_transcription": false, "audio": false, "generation_complete": false, "turn_complete": false}
		result[name] = flags
		if response.SendControlledTurn(turnCtx, lead, directive) != nil {
			t.Fatal("controlled send failed")
		}
		flags["send"] = true
		for !flags["turn_complete"] {
			e, err := response.Receive(turnCtx)
			if err != nil {
				t.Fatal("controlled receive failed")
			}
			switch e.Kind {
			case geminilive.EventOutputTranscription:
				flags["output_transcription"] = true
			case geminilive.EventAudio:
				flags["audio"] = flags["audio"] || len(e.Audio) > 0
			case geminilive.EventGenerationComplete:
				flags["generation_complete"] = true
			case geminilive.EventTurnComplete:
				flags["turn_complete"] = true
			case geminilive.EventAPIError, geminilive.EventClosed:
				t.Fatal("provider terminated before turn completion")
			}
		}
		for key, ok := range flags {
			if !ok {
				t.Errorf("%s missing %s", name, key)
			}
		}
	}
	run("isolated", conversation.TurnDirective{Kind: conversation.ActionContinueConversation, Reason: conversation.ReasonContinueDiscovery})
	jc, err := openrouterjev.ConfigFromEnv()
	if err != nil {
		t.Fatal("JEV configuration unavailable")
	}
	jev, err := openrouterjev.NewWithTimeout(jc, 1500*time.Millisecond)
	if err != nil {
		t.Fatal("JEV unavailable")
	}
	input, err := testDecisionInput(lead, "active")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := jev.Decide(ctx, input)
	result["jev"] = err == nil
	if err != nil {
		t.Fatal("JEV failed without retry")
	}
	directive, err := conversation.BuildTurnDirective(conversation.OrchestratorInput{DecisionInput: input, Decision: decision})
	if err != nil {
		t.Fatal(err)
	}
	run("jev_gemini", directive)
}
