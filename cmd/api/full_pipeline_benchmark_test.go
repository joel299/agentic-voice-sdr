package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
	promptpostgres "github.com/joel299/agentic-voice-sdr/internal/integrations/postgres/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/sessionprompt"
	"github.com/joel299/agentic-voice-sdr/internal/telemetry"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
	"github.com/joel299/agentic-voice-sdr/internal/toolruntime"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
	"github.com/joel299/agentic-voice-sdr/internal/voiceflow"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureTimedInput struct {
	geminilive.InputTranscriberSession
	end     *atomic.Int64
	timings *telemetry.TurnCollector
}

func (s *fixtureTimedInput) Receive(ctx context.Context) (geminilive.TranscriptEvent, error) {
	v, e := s.InputTranscriberSession.Receive(ctx)
	if e == nil && v.State == geminilive.TranscriptFinal {
		_, tr := s.timings.Begin(ctx, v.TurnID, time.Now())
		if end := s.end.Load(); end > 0 {
			tr.Mark("lead_speech_end_at", time.Unix(0, end))
			tr.SetSpeechEndBasis("fixture_known_end")
		}
	}
	return v, e
}

// Includes paced synthetic RX, both real Gemini sessions, real JEV,
// FinalTranscriptHandler/TurnRuntime/SplitBridge, Go IPC and the loaded C .so.
// Stops after C first-audio evidence; never uses SIP, dial, accounts or RTP.
func TestRealNoCallFullPipelineFirstAudio(t *testing.T) {
	out := os.Getenv("GRU152_FULL_PIPELINE_OUTPUT")
	if out == "" {
		t.Skip("explicit real-provider + actual C no-call gate")
	}
	bin, module := os.Getenv("BARESIP_C_TEST_BIN"), os.Getenv("BARESIP_C_TEST_MODULE")
	if bin == "" || module == "" {
		t.Fatal("actual C source required")
	}
	root, _ := filepath.Abs("../..")
	if e := config.LoadLocalEnv(filepath.Join(root, ".env")); e != nil {
		t.Fatal("private env unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, e := pgxpool.New(ctx, "")
	if e != nil {
		t.Fatal("database unavailable")
	}
	defer pool.Close()
	repo, _ := promptpostgres.NewRepository(pool)
	ps, _ := agentprompt.NewPromptService(repo)
	var results []map[string]any
	persist := func() {
		data, _ := json.MarshalIndent(results, "", "  ")
		if e := os.WriteFile(out, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	defer persist()
	variants := []string{"before", "after"}
	if os.Getenv("GRU152_FULL_PIPELINE_VARIANT") == "after" {
		variants = []string{"after"}
	}
	for _, variant := range variants {
		fixtures := []string{"short", "normal", "natural_pause", "yes", "one_second_internal_pause"}
		if os.Getenv("GRU152_FULL_PIPELINE_FIXTURE") == "yes" {
			fixtures = []string{"yes"}
		}
		for _, fixture := range fixtures {
			func() {
				c, stop := context.WithTimeout(ctx, 35*time.Second)
				defer stop()
				wav, e := os.ReadFile(filepath.Join(root, ".runtime", "vad-fixtures", fixture+".wav"))
				if e != nil || len(wav) < 45 {
					t.Fatal("fixture unavailable")
				}
				pcm := wav[44:]
				mediaCfg := baresipmedia.Config{ParentDir: "/tmp", BufferFrames: 32}
				if variant == "after" {
					mediaCfg.BufferFrames = 2
					mediaCfg.RXBufferFrames = 4
					mediaCfg.TXSocketBufferBytes = 1024
				}
				adapter, e := baresipmedia.New(c, mediaCfg)
				if e != nil {
					t.Fatal(e)
				}
				defer adapter.Close()
				rxPath, txPath := adapter.SocketPaths()
				rx, e := net.Dial("unix", rxPath)
				if e != nil {
					t.Fatal(e)
				}
				defer rx.Close()
				command := exec.CommandContext(c, bin, module, "1", "--external", txPath, "--observe")
				command.Env = append(os.Environ(), "GRU151_MEDIA_TIMING_ACK=1", "GRU151_MEDIA_FLUSH=1")
				var sourceOutput bytes.Buffer
				command.Stdout = &sourceOutput
				command.Stderr = &sourceOutput
				if e = command.Start(); e != nil {
					t.Fatal(e)
				}
				defer command.Process.Kill()
				session, e := adapter.WaitSession(c)
				if e != nil {
					t.Fatal(e)
				}
				defer session.Close()
				gc := geminilive.ConfigFromEnv()
				gc.VoiceName = "Kore"
				gc.VoiceDescription = "Voz comercial brasileira, humana e consultiva. Deve transmitir clareza, proximidade e confiança sem parecer locução publicitária."
				gc.VoiceStyle = "Calmo e confiante. Ritmo moderado. Frases curtas. Tom acolhedor e profissional."
				if variant == "after" {
					gc.VoiceName = "Fola"
					gc.VoiceStyle = "PT-BR natural e consultivo. Responda em uma ou duas frases curtas, com uma pergunta por vez. <breath> indica uma respiração discreta numa pausa natural; não pronuncie a marcação nem acrescente pausas longas."
				}
				builder, _ := sessionprompt.NewBuilder(gc, immutableGeminiCore, ps)
				gc, _, e = builder.Build(c)
				if e != nil {
					t.Fatal("prompt unavailable")
				}
				input, e := geminilive.ConnectInputTranscriber(c, gc)
				if e != nil {
					results = append(results, map[string]any{"variant": variant, "fixture": fixture, "ok": false, "failure": "input_connect_failed"})
					t.Log("input connect failed, no retry")
					return
				}
				defer input.Close()
				response, e := geminilive.ConnectControlledResponse(c, gc)
				if e != nil {
					results = append(results, map[string]any{"variant": variant, "fixture": fixture, "ok": false, "failure": "response_connect_failed"})
					t.Log("response connect failed, no retry")
					return
				}
				defer response.Close()
				jc, e := openrouterjev.ConfigFromEnv()
				if e != nil {
					t.Fatal("JEV unavailable")
				}
				j, _ := openrouterjev.NewWithTimeout(jc, 1500*time.Millisecond)
				processor, _ := turnruntime.New(j, toolruntime.NewDispatcher(tools.NewInMemoryRegistry(), toolruntime.NewExecutorRegistry(nil)))
				state, _ := conversation.NewConversationState("fixture")
				timings := telemetry.NewTurnCollector(16)
				var speechEnd atomic.Int64
				timed := &fixtureTimedInput{input, &speechEnd, timings}
				bridge, e := voiceflow.NewSplitRuntime(session, session, timed, response, state, processor, conversation.NewResponseGate(), nil, nil)
				if e != nil {
					t.Fatal(e)
				}
				bridge.SetTurnCollector(timings)
				bridgeDone := make(chan error, 1)
				go func() { bridgeDone <- bridge.Run(c) }()
				producerDone := make(chan struct{})
				go func() {
					defer close(producerDone)
					began := time.Now()
					speechEnd.Store(began.Add(time.Duration(len(pcm)) * time.Second / 32000).UnixNano())
					frames := append(append([]byte(nil), pcm...), make([]byte, 32000*4)...)
					for offset := 0; offset < len(frames); offset += 640 {
						n := min(640, len(frames)-offset)
						timer := time.NewTimer(time.Until(began.Add(time.Duration(offset) * time.Second / 32000)))
						select {
						case <-timer.C:
						case <-c.Done():
							timer.Stop()
							return
						}
						wire, _ := audiosocket.Encode(audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: frames[offset : offset+n]})
						if _, e := rx.Write(wire); e != nil {
							return
						}
					}
				}()
				sourceErr := command.Wait()
				stop()
				bridgeErr := <-bridgeDone
				<-producerDone
				row := map[string]any{"bridge_failure_class": aiFailureClass(bridgeErr, aiFailureStage(bridgeErr)), "bridge_failure_stage": aiFailureStage(bridgeErr), "variant": variant, "fixture": fixture, "actual_c_source_ok": sourceErr == nil, "real_call_count": 0, "retry_count": 0}
				snaps := timings.Snapshot()
				for _, s := range snaps {
					if !s.Times["c_source_first_real_frame_at"].IsZero() {
						row["times"] = s
						row["metrics_ms"] = s.Metrics()
						break
					}
				}
				row["media_metrics"] = session.Metrics()
				row["lead_final_count"] = len(snaps)
				if metrics, ok := row["metrics_ms"].(map[string]float64); ok {
					_, valid := metrics["speech_end_to_final_transcript_ms"]
					row["speech_end_valid"] = valid
				}
				results = append(results, row)
				persist()
				t.Logf("variant=%s fixture=%s actual_c=%v timings=%v", variant, fixture, sourceErr == nil, row["metrics_ms"])
			}()
		}
	}
	data, _ := json.MarshalIndent(results, "", "  ")
	if e = os.WriteFile(out, data, 0600); e != nil {
		t.Fatal(e)
	}
	for _, row := range results {
		if row["actual_c_source_ok"] != true || row["speech_end_valid"] != true {
			t.Error("full audio-to-C gate incomplete or premature FINAL; evidence retained, no retry")
		}
	}
}
