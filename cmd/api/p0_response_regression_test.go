package main

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
	pg "github.com/joel299/agentic-voice-sdr/internal/integrations/postgres/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/sessionprompt"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
	"github.com/joel299/agentic-voice-sdr/internal/toolruntime"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
	"github.com/joel299/agentic-voice-sdr/internal/voiceflow"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countInput struct {
	geminilive.InputTranscriberSession
	finals atomic.Int64
}

func (i *countInput) Receive(c context.Context) (geminilive.TranscriptEvent, error) {
	v, e := i.InputTranscriberSession.Receive(c)
	if e == nil && v.State == geminilive.TranscriptFinal {
		i.finals.Add(1)
	}
	return v, e
}

type countJEV struct {
	conversation.DecisionProvider
	calls atomic.Int64
}

func (j *countJEV) Decide(c context.Context, i conversation.DecisionInput) (conversation.Decision, error) {
	j.calls.Add(1)
	return j.DecisionProvider.Decide(c, i)
}
func TestRealNoCallStartupResponseRegression(t *testing.T) {
	output := os.Getenv("GRU152_P0_REGRESSION_OUTPUT")
	if output == "" {
		t.Skip("explicit paid provider no-call startup regression gate")
	}
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	if config.LoadLocalEnv(root+"/.env") != nil {
		t.Fatal("protected env unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	result := map[string]any{"real_calls": 0, "retry": 0}
	defer func() {
		b, _ := json.MarshalIndent(result, "", "  ")
		if os.WriteFile(output, b, 0600) != nil {
			t.Error("metadata output unavailable")
		}
		if result["turn_complete"] != true || result["lead_final_count"] != int64(1) || result["jev_count"] != int64(1) || result["audio_events"] == int64(0) || result["tx_frames"] == int64(0) {
			t.Error("functional startup gate failed; evidence retained, no retry")
		}
	}()
	pool, e := pgxpool.New(ctx, "")
	if e != nil {
		t.Fatal("database unavailable")
	}
	defer pool.Close()
	repo, _ := pg.NewRepository(pool)
	ps, _ := agentprompt.NewPromptService(repo)
	cfg := geminilive.ConfigFromEnv()
	cfg.VoiceName = "Fola"
	cfg.VoiceDescription = "Voz comercial brasileira, humana e consultiva. Deve transmitir clareza, proximidade e confiança sem parecer locução publicitária."
	cfg.VoiceStyle = "PT-BR natural e consultivo. Responda em uma ou duas frases curtas, com uma pergunta por vez. <breath> indica uma respiração discreta numa pausa natural; não pronuncie a marcação nem acrescente pausas longas."
	builder, _ := sessionprompt.NewBuilder(cfg, immutableGeminiCore, ps)
	cfg, _, e = builder.Build(ctx)
	if e != nil {
		t.Fatal("prompt unavailable")
	}
	input, e := geminilive.ConnectInputTranscriber(ctx, cfg)
	if e != nil {
		result["failure"] = "input_connect"
		return
	}
	defer input.Close()
	response, e := geminilive.ConnectControlledResponse(ctx, cfg)
	if e != nil {
		result["failure"] = "response_connect"
		return
	}
	defer response.Close()
	jc, e := openrouterjev.ConfigFromEnv()
	if e != nil {
		t.Fatal("JEV unavailable")
	}
	j, e := openrouterjev.NewWithTimeout(jc, 1500*time.Millisecond)
	if e != nil {
		t.Fatal("JEV unavailable")
	}
	ci := &countInput{InputTranscriberSession: input}
	cj := &countJEV{DecisionProvider: j}
	processor, _ := turnruntime.New(cj, toolruntime.NewDispatcher(tools.NewInMemoryRegistry(), toolruntime.NewExecutorRegistry(nil)))
	state, _ := conversation.NewConversationState("probe-no-call")
	mc := localBaresipMediaConfig()
	mc.ParentDir = "/tmp"
	adapter, e := baresipmedia.New(ctx, mc)
	if e != nil {
		t.Fatal("adapter unavailable")
	}
	defer adapter.Close()
	rxPath, txPath := adapter.SocketPaths()
	rx, e := net.Dial("unix", rxPath)
	if e != nil {
		t.Fatal("RX unavailable")
	}
	defer rx.Close()
	tx, e := net.Dial("unix", txPath)
	if e != nil {
		t.Fatal("TX unavailable")
	}
	defer tx.Close()
	session, e := adapter.WaitSession(ctx)
	if e != nil {
		t.Fatal("session unavailable")
	}
	defer session.Close()
	var txFrames atomic.Int64
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			f, e := audiosocket.DecodeReader(tx)
			if e != nil {
				return
			}
			if f.Type == audiosocket.TypeSlin24 {
				txFrames.Add(1)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	completed := make(chan struct{})
	var once sync.Once
	var audio, ot atomic.Int64
	event := func(_ context.Context, e geminilive.Event) error {
		switch e.Kind {
		case geminilive.EventAudio:
			audio.Add(1)
		case geminilive.EventOutputTranscription:
			ot.Add(1)
		case geminilive.EventTurnComplete:
			once.Do(func() { close(completed) })
		}
		return nil
	}
	b, e := voiceflow.NewSplitRuntime(session, session, ci, response, state, processor, conversation.NewResponseGate(), nil, bridge.EventHandler(event))
	if e != nil {
		t.Fatal("bridge unavailable")
	}
	done := make(chan error, 1)
	{
		// Reproduce speech buffered while provider setup delays the consumer.
		w, e := os.ReadFile(root + "/.runtime/vad-fixtures/yes.wav")
		if e != nil || len(w) < 44 {
			t.Fatal("startup fixture unavailable")
		}
		early := append(append([]byte(nil), w[44:]...), make([]byte, 3840)...)
		start := time.Now()
		for off := 0; off < len(early); off += 640 {
			time.Sleep(time.Until(start.Add(time.Duration(off) * time.Second / 32000)))
			wire, _ := audiosocket.Encode(audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: early[off:min(off+640, len(early))]})
			if _, e := rx.Write(wire); e != nil {
				t.Fatal("startup RX failed")
			}
		}
		time.Sleep(20 * time.Millisecond)
		result["startup_before_consumer_ms"] = time.Since(start).Milliseconds()
		result["startup_media_metrics"] = session.Metrics()
	}
	go func() { done <- b.Run(ctx) }()
	pcm := make([]byte, 32000*12) // silence permits automatic VAD finalization.
	producer := make(chan struct{})
	go func() {
		defer close(producer)
		start := time.Now()
		for offset := 0; offset < len(pcm); offset += 640 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(start.Add(time.Duration(offset) * time.Second / 32000))):
			}
			wire, _ := audiosocket.Encode(audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: pcm[offset:min(offset+640, len(pcm))]})
			if _, e := rx.Write(wire); e != nil {
				return
			}
		}
	}()
	select {
	case <-completed:
		result["turn_complete"] = true
	case <-ctx.Done():
		result["turn_complete"] = false
	}
	result["lead_final_count"] = ci.finals.Load()
	result["jev_count"] = cj.calls.Load()
	result["audio_events"] = audio.Load()
	result["output_transcription_events"] = ot.Load()
	result["tx_frames"] = txFrames.Load()
	result["media_metrics"] = session.Metrics()
	cancel()
	<-done
	rx.Close()
	tx.Close()
	<-producer
	<-drainDone
}
