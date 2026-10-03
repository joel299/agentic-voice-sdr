package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"golang.org/x/text/unicode/norm"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestRealNoCallVADBenchmark(t *testing.T) {
	out := os.Getenv("GRU152_VAD_OUTPUT")
	if out == "" {
		t.Skip("explicit provider-only VAD fixture gate")
	}
	root, _ := filepath.Abs("../..")
	if e := config.LoadLocalEnv(filepath.Join(root, ".env")); e != nil {
		t.Fatal("private environment unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cfg := geminilive.ConfigFromEnv()
	cfg.VoiceName = "Kore"
	cfg.SystemInstruction = "Você gera fixtures de fala para teste. Leia exatamente a frase solicitada em português brasileiro, sem responder, sem adicionar nenhuma palavra."
	r, e := geminilive.ConnectControlledResponse(ctx, cfg)
	if e != nil {
		t.Fatal("fixture connection failed")
	}
	defer r.Close()
	references := map[string]string{}
	speech := func(text string) []byte {
		c, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		d := conversation.TurnDirective{Kind: conversation.ActionAskQuestion, Reason: conversation.ReasonNeedsClarification}
		if e := r.SendControlledTurn(c, "Leia exatamente: "+text, d); e != nil {
			t.Fatal("fixture send failed")
		}
		var p []byte
		for {
			v, e := r.Receive(c)
			if e != nil {
				t.Fatal("fixture receive failed")
			}
			if v.Kind == geminilive.EventOutputTranscription {
				references[text] += v.Text
			}
			if v.Kind == geminilive.EventAudio {
				p = append(p, v.Audio...)
			}
			if v.Kind == geminilive.EventTurnComplete {
				break
			}
		}
		if len(p) == 0 {
			t.Fatal("empty fixture")
		} // resample offline only, 24k -> 16k linear interpolation
		n := len(p) / 2
		var dst []byte
		for i := 0; i*3/2+1 < n; i++ {
			pos := i * 3
			idx := pos / 2
			a := int32(int16(binary.LittleEndian.Uint16(p[2*idx:])))
			b := int32(int16(binary.LittleEndian.Uint16(p[2*(idx+1):])))
			v := a
			if pos%2 != 0 {
				v = (a + b) / 2
			}
			dst = binary.LittleEndian.AppendUint16(dst, uint16(int16(v)))
		} // trim only final/initial silence using a fixed fixture amplitude boundary
		start, end := 0, len(dst)
		for start+2 < end && fixtureAbs(dst[start:]) < 300 {
			start += 2
		}
		for end-2 > start && fixtureAbs(dst[end-2:]) < 300 {
			end -= 2
		}
		if start > 6400 {
			start -= 6400
		} else {
			start = 0
		}
		return dst[start:end]
	}
	fixtures := []struct {
		name     string
		pcm      []byte
		expected []string
	}{{"short", speech("Sim, podemos conversar."), []string{"sim", "conversar"}}, {"normal", speech("Quero entender como vocês ajudam minha equipe de vendas."), []string{"entender", "vendas"}}, {"natural_pause", append(append(speech("Quero entender"), make([]byte, 11200)...), speech("como funciona")...), []string{"entender", "funciona"}}, {"yes", speech("Sim."), []string{"sim"}}, {"one_second_internal_pause", append(append(speech("Preciso conversar com meu sócio"), make([]byte, 32000)...), speech("antes de decidir")...), []string{"sócio", "decidir"}}}
	fixtureDir := filepath.Join(root, ".runtime", "vad-fixtures")
	os.MkdirAll(fixtureDir, 0700)
	for _, f := range fixtures {
		b := make([]byte, 44)
		copy(b, "RIFF")
		binary.LittleEndian.PutUint32(b[4:], uint32(len(f.pcm)+36))
		copy(b[8:], "WAVEfmt ")
		binary.LittleEndian.PutUint32(b[16:], 16)
		binary.LittleEndian.PutUint16(b[20:], 1)
		binary.LittleEndian.PutUint16(b[22:], 1)
		binary.LittleEndian.PutUint32(b[24:], 16000)
		binary.LittleEndian.PutUint32(b[28:], 32000)
		binary.LittleEndian.PutUint16(b[32:], 2)
		binary.LittleEndian.PutUint16(b[34:], 16)
		copy(b[36:], "data")
		binary.LittleEndian.PutUint32(b[40:], uint32(len(f.pcm)))
		os.WriteFile(filepath.Join(fixtureDir, f.name+".wav"), append(b, f.pcm...), 0600)
	}
	if os.Getenv("GRU152_FIXTURE_ONLY") == "true" {
		return
	}
	variants := []struct {
		name    string
		silence int
		hybrid  bool
	}{{"default", 0, false}, {"auto_500", 500, false}, {"auto_800", 800, false}, {"auto_1200", 1200, false}, {"hybrid_1200", 1200, true}}
	var results []map[string]any
	for _, v := range variants {
		for _, f := range fixtures {
			c, stop := context.WithTimeout(ctx, 18*time.Second)
			conf := cfg
			conf.SystemInstruction = "Transcreva a fala de entrada fielmente em português brasileiro."
			conf.VAD = geminilive.VADConfig{SilenceDurationMS: v.silence, PrefixPaddingMS: 40, EndSensitivity: "END_SENSITIVITY_LOW", Hybrid: v.hybrid}
			input, e := geminilive.ConnectInputTranscriber(c, conf)
			if e != nil {
				stop()
				t.Fatal("VAD setup failed")
			}
			type received struct {
				at   time.Time
				text string
			}
			events := make(chan received, 32)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					ev, e := input.Receive(c)
					if e != nil {
						return
					}
					if ev.State == geminilive.TranscriptFinal {
						select {
						case events <- received{time.Now(), ev.Text}:
						case <-c.Done():
							return
						}
					}
				}
			}()
			p := append(append([]byte(nil), f.pcm...), make([]byte, 3*32000)...)
			began := time.Now()
			speechEnd := began.Add(time.Duration(len(f.pcm)) * time.Second / 32000)
			sendOK := true
			for offset := 0; offset < len(p); offset += 640 {
				end := offset + 640
				if end > len(p) {
					end = len(p)
				}
				timer := time.NewTimer(time.Until(began.Add(time.Duration(offset) * time.Second / 32000)))
				select {
				case <-timer.C:
				case <-c.Done():
					timer.Stop()
					sendOK = false
				}
				if !sendOK {
					break
				}
				if e = input.SendAudio(c, p[offset:end]); e != nil {
					sendOK = false
					break
				}
			}
			// Allow a flush to complete without inventing a retry or resending speech.
			if v.hybrid {
				timer := time.NewTimer(3 * time.Second)
				select {
				case <-timer.C:
				case <-c.Done():
					timer.Stop()
				}
			}
			stop()
			input.Close()
			<-done
			close(events)
			var text string
			var last time.Time
			early := 0
			count := 0
			for e := range events {
				count++
				text += " " + e.text
				if e.at.Before(speechEnd) {
					early++
				}
				last = e.at
			}
			matched := 0
			for _, word := range f.expected {
				if strings.Contains(normalizeFixtureText(text), normalizeFixtureText(word)) {
					matched++
				}
			}
			row := map[string]any{"variant": v.name, "fixture": f.name, "send_ok": sendOK, "final_count": count, "false_end_count": early, "expected_keywords": len(f.expected), "matched_keywords": matched, "word_clipping": matched != len(f.expected), "transcription_accuracy": float64(matched) / float64(len(f.expected))}
			if !last.IsZero() {
				row["final_transcript_latency_ms"] = last.Sub(speechEnd).Milliseconds()
			}
			row["synthetic_transcript"] = text
			results = append(results, row)
			public := map[string]any{}
			for k, v := range row {
				if k != "synthetic_transcript" {
					public[k] = v
				}
			}
			t.Log(public)
		}
	}
	data, _ := json.MarshalIndent(map[string]any{"results": results, "synthetic_references": references}, "", "  ")
	if e = os.WriteFile(out, data, 0600); e != nil {
		t.Fatal(e)
	}
}
func fixtureAbs(b []byte) int32 {
	v := int32(int16(binary.LittleEndian.Uint16(b)))
	if v < 0 {
		return -v
	}
	return v
}

func normalizeFixtureText(s string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, norm.NFD.String(s)))
}
