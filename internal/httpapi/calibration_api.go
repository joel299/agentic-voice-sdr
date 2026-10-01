package httpapi

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
)

const CanonicalJEVTimeoutMS = 400

type JEVSettings struct {
	Model            string `json:"model"`
	TimeoutMS        int    `json:"timeout_ms"`
	Description      string `json:"description"`
	DecisionGuidance string `json:"decision_guidance"`
}
type GeminiSettings struct {
	Model       string `json:"model"`
	VoiceName   string `json:"voice_name"`
	Description string `json:"description"`
	Style       string `json:"style"`
}
type TuningStore struct {
	mu     sync.RWMutex
	jev    JEVSettings
	gemini GeminiSettings
}

func NewTuningStore(jev JEVSettings, gemini GeminiSettings) (*TuningStore, error) {
	if jev.TimeoutMS == 0 {
		jev.TimeoutMS = CanonicalJEVTimeoutMS
	}
	if err := validateJEV(jev); err != nil {
		return nil, err
	}
	if err := validateGemini(gemini); err != nil {
		return nil, err
	}
	return &TuningStore{jev: jev, gemini: gemini}, nil
}
func (s *TuningStore) JEV() JEVSettings       { s.mu.RLock(); defer s.mu.RUnlock(); return s.jev }
func (s *TuningStore) Gemini() GeminiSettings { s.mu.RLock(); defer s.mu.RUnlock(); return s.gemini }
func (s *TuningStore) SetJEV(v JEVSettings) error {
	if err := validateJEV(v); err != nil {
		return err
	}
	s.mu.Lock()
	s.jev = v
	s.mu.Unlock()
	return nil
}
func (s *TuningStore) SetGemini(v GeminiSettings) error {
	if err := validateGemini(v); err != nil {
		return err
	}
	s.mu.Lock()
	s.gemini = v
	s.mu.Unlock()
	return nil
}
func validateJEV(v JEVSettings) error {
	if strings.TrimSpace(v.Model) == "" || len(v.Model) > 160 || v.TimeoutMS < 50 || v.TimeoutMS > 5000 || len(v.Description) > 2000 || len(v.DecisionGuidance) > 4000 {
		return ErrInvalidTuning
	}
	return nil
}
func validateGemini(v GeminiSettings) error {
	if strings.TrimSpace(v.Model) == "" || len(v.Model) > 160 || strings.TrimSpace(v.VoiceName) == "" || len(v.VoiceName) > 80 || len(v.Description) > 2000 || len(v.Style) > 4000 {
		return ErrInvalidTuning
	}
	return nil
}

var ErrInvalidTuning = errors.New("invalid runtime tuning")

type JEVTestResult struct {
	Intent              string `json:"intent"`
	NextAction          string `json:"next_action"`
	Reason              string `json:"reason"`
	LatencyMS           int64  `json:"latency_ms"`
	TimeoutMS           int    `json:"timeout_ms"`
	ProviderStatusClass string `json:"provider_status_class"`
}
type AgentTurnResult struct {
	TestID   string             `json:"test_id"`
	Prompt   PromptIdentity     `json:"prompt"`
	JEV      JEVTestResult      `json:"jev"`
	Gemini   GeminiTurnMetadata `json:"gemini"`
	AudioURL string             `json:"audio_url"`
}
type PromptIdentity struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}
type GeminiTurnMetadata struct {
	OutputTranscription string `json:"output_transcription"`
	FirstAudioMS        int64  `json:"first_audio_ms"`
	LastAudioMS         int64  `json:"last_audio_ms"`
	TurnCompleteMS      int64  `json:"turn_complete_ms"`
	TotalTurnMS         int64  `json:"total_turn_ms"`
	GenerationComplete  bool   `json:"generation_complete"`
	TurnComplete        bool   `json:"turn_complete"`
	AudioEventCount     int    `json:"audio_event_count"`
	AudioBytesTotal     int    `json:"audio_bytes_total"`
	TransportClass      string `json:"transport_class"`
}
type CalibrationServices struct {
	Prompts       AgentPromptManager
	Tuning        *TuningStore
	FalePacoSIP   FalePacoSIPProfileService
	TestJEV       func(context.Context, string, string) (JEVTestResult, error)
	TestAgentTurn func(context.Context, string, string, string) (AgentTurnResult, []byte, error)
	RuntimeStatus func(context.Context) (map[string]any, error)
}
type calibrationAPI struct {
	deps  CalibrationServices
	audio *ephemeralAudioStore
}

func registerCalibrationRoutes(r chi.Router, auth OwnerAuthorizer, deps CalibrationServices) {
	if deps.FalePacoSIP != nil {
		sip := &falePacoSIPAPI{service: deps.FalePacoSIP}
		r.Route("/v1/config/sip-trunk", func(p chi.Router) {
			p.Use(ownerOnly(auth))
			p.Get("/", sip.get)
			p.Put("/", sip.put)
			p.Post("/validate", sip.validate)
		})
	}
	if deps.Tuning == nil {
		return
	}
	a := &calibrationAPI{deps: deps, audio: newEphemeralAudioStore()}
	r.Route("/api/v1/agent/prompt", func(p chi.Router) {
		p.Use(ownerOnly(auth))
		p.Get("/", a.getPrompt)
		p.Put("/", a.putPrompt)
		p.Get("/versions", a.promptVersions)
		p.Post("/versions/{version}/activate", a.activatePrompt)
	})
	r.Route("/v1", func(v chi.Router) {
		v.Route("/config/jev", func(x chi.Router) { x.Use(ownerOnly(auth)); x.Get("/", a.getJEV); x.Put("/", a.putJEV) })
		v.Route("/config/gemini", func(x chi.Router) { x.Use(ownerOnly(auth)); x.Get("/", a.getGemini); x.Put("/", a.putGemini) })
		v.Route("/test", func(x chi.Router) {
			x.Use(ownerOnly(auth))
			x.Post("/jev", a.testJEV)
			x.Post("/agent-turn", a.testAgentTurn)
			x.Get("/agent-turn/{test_id}/audio", a.getAudio)
		})
		v.With(ownerOnly(auth)).Get("/runtime/status", a.runtimeStatus)
	})
}
func (a *calibrationAPI) getPrompt(w http.ResponseWriter, r *http.Request) {
	p, e := a.deps.Prompts.GetActive(r.Context())
	if e != nil {
		writeAgentPromptError(w, e)
		return
	}
	writeJSON(w, 200, toAgentPromptResponse(p))
}
func (a *calibrationAPI) putPrompt(w http.ResponseWriter, r *http.Request) {
	var q agentPromptPutRequest
	if decodeJSON(w, r, &q) != nil {
		return
	}
	p, e := a.deps.Prompts.CreateAndActivate(r.Context(), agentprompt.PromptDraft{Name: q.Name, Content: q.Prompt})
	if e != nil {
		writeAgentPromptError(w, e)
		return
	}
	writeJSON(w, 201, toAgentPromptResponse(p))
}
func (a *calibrationAPI) promptVersions(w http.ResponseWriter, r *http.Request) {
	v, e := a.deps.Prompts.ListVersions(r.Context())
	if e != nil {
		writeAgentPromptError(w, e)
		return
	}
	out := make([]agentPromptResponse, 0, len(v))
	for _, p := range v {
		out = append(out, toAgentPromptResponse(p))
	}
	writeJSON(w, 200, map[string]any{"versions": out})
}
func (a *calibrationAPI) activatePrompt(w http.ResponseWriter, r *http.Request) {
	h := &agentPromptHandler{manager: a.deps.Prompts}
	h.activateVersion(w, r)
}
func (a *calibrationAPI) getJEV(w http.ResponseWriter, _ *http.Request) {
	v := a.deps.Tuning.JEV()
	writeJSON(w, 200, map[string]any{"model": v.Model, "configured_timeout_ms": v.TimeoutMS, "canonical_default_timeout_ms": CanonicalJEVTimeoutMS, "description": v.Description, "decision_guidance": v.DecisionGuidance})
}
func (a *calibrationAPI) putJEV(w http.ResponseWriter, r *http.Request) {
	var v JEVSettings
	if decodeJSON(w, r, &v) != nil {
		return
	}
	if e := a.deps.Tuning.SetJEV(v); e != nil {
		writeJSON(w, 422, map[string]string{"error": "invalid JEV tuning; timeout_ms must be 50..5000"})
		return
	}
	a.getJEV(w, r)
}
func (a *calibrationAPI) getGemini(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, a.deps.Tuning.Gemini())
}
func (a *calibrationAPI) putGemini(w http.ResponseWriter, r *http.Request) {
	var v GeminiSettings
	if decodeJSON(w, r, &v) != nil {
		return
	}
	if e := a.deps.Tuning.SetGemini(v); e != nil {
		writeJSON(w, 422, map[string]string{"error": "invalid Gemini tuning"})
		return
	}
	a.getGemini(w, r)
}

type testLeadRequest struct {
	LeadText    string `json:"lead_text"`
	Stage       string `json:"stage,omitempty"`
	Description string `json:"description,omitempty"`
	Style       string `json:"style,omitempty"`
}
type agentTurnRequest struct {
	LeadText    string `json:"lead_text"`
	Description string `json:"description,omitempty"`
	Style       string `json:"style,omitempty"`
}

func (a *calibrationAPI) testJEV(w http.ResponseWriter, r *http.Request) {
	var q testLeadRequest
	if decodeJSON(w, r, &q) != nil {
		return
	}
	if strings.TrimSpace(q.LeadText) == "" || len(q.LeadText) > 8192 {
		writeJSON(w, 400, map[string]string{"error": "lead_text is required and limited to 8192 bytes"})
		return
	}
	if q.Stage != "" && q.Stage != "opening" && q.Stage != "active" && q.Stage != "closing" && q.Stage != "ended" {
		writeJSON(w, 422, map[string]string{"error": "stage must be opening, active, closing, or ended"})
		return
	}
	if a.deps.TestJEV == nil {
		writeJSON(w, 503, map[string]string{"error": "JEV test unavailable"})
		return
	}
	v, e := a.deps.TestJEV(r.Context(), q.LeadText, q.Stage)
	if e != nil {
		writeProviderError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (a *calibrationAPI) testAgentTurn(w http.ResponseWriter, r *http.Request) {
	var q agentTurnRequest
	if decodeJSON(w, r, &q) != nil {
		return
	}
	if strings.TrimSpace(q.LeadText) == "" || len(q.LeadText) > 8192 {
		writeJSON(w, 400, map[string]string{"error": "lead_text is required and limited to 8192 bytes"})
		return
	}
	if a.deps.TestAgentTurn == nil {
		writeJSON(w, 503, map[string]string{"error": "agent turn test unavailable"})
		return
	}
	result, pcm, e := a.deps.TestAgentTurn(r.Context(), q.LeadText, q.Description, q.Style)
	if e != nil {
		writeProviderError(w, e)
		return
	}
	if !result.Gemini.GenerationComplete || !result.Gemini.TurnComplete || len(pcm) == 0 {
		writeJSON(w, 502, map[string]string{"error": "Gemini diagnostic turn did not complete"})
		return
	}
	if len(pcm)%2 != 0 {
		writeJSON(w, 502, map[string]string{"error": "invalid Gemini PCM output"})
		return
	}
	if !a.audio.put(result.TestID, pcm) {
		writeJSON(w, 502, map[string]string{"error": "Gemini diagnostic audio exceeds the 12 MiB per-test limit"})
		return
	}
	result.AudioURL = "/v1/test/agent-turn/" + result.TestID + "/audio"
	writeJSON(w, 200, result)
}
func (a *calibrationAPI) getAudio(w http.ResponseWriter, r *http.Request) {
	data, ok := a.audio.get(chi.URLParam(r, "test_id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "audio test not found or expired"})
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", "inline; filename=agent-turn.wav")
	w.WriteHeader(200)
	_, _ = w.Write(wav24k(data))
}
func (a *calibrationAPI) runtimeStatus(w http.ResponseWriter, r *http.Request) {
	if a.deps.RuntimeStatus == nil {
		writeJSON(w, 503, map[string]string{"error": "runtime status unavailable"})
		return
	}
	v, e := a.deps.RuntimeStatus(r.Context())
	if e != nil {
		writeJSON(w, 503, map[string]string{"error": "runtime status unavailable"})
		return
	}
	writeJSON(w, 200, v)
}

type safeProviderClass interface{ ProviderStatusClass() string }

func writeProviderError(w http.ResponseWriter, e error) {
	code := 502
	msg := "provider request failed"
	class := "provider_failure"
	if safe, ok := e.(safeProviderClass); ok && safe.ProviderStatusClass() != "" {
		class = safe.ProviderStatusClass()
	}
	var geminiErr *geminilive.Error
	if errors.As(e, &geminiErr) {
		class = string(geminiErr.Kind)
		if geminiErr.TransportClass != "" {
			class += "_" + string(geminiErr.TransportClass)
		} else if geminiErr.CloseStatusClass != "" {
			class += "_" + string(geminiErr.CloseStatusClass)
		}
	}
	if errors.Is(e, context.DeadlineExceeded) || strings.Contains(strings.ToLower(e.Error()), "timed out") {
		code = 504
		msg = "provider request timed out"
		class = "provider_timeout"
	}
	writeJSON(w, code, map[string]string{"error": msg, "provider_status_class": class})
}

type audioItem struct {
	pcm     []byte
	expires time.Time
	timer   *time.Timer
}
type ephemeralAudioStore struct {
	mu    sync.Mutex
	items map[string]audioItem
	bytes int
}

func newEphemeralAudioStore() *ephemeralAudioStore {
	return &ephemeralAudioStore{items: map[string]audioItem{}}
}
func (s *ephemeralAudioStore) put(id string, pcm []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if id == "" || len(pcm) > 12<<20 {
		return false
	}
	if old, ok := s.items[id]; ok {
		if old.timer != nil {
			old.timer.Stop()
		}
		s.bytes -= len(old.pcm)
		delete(s.items, id)
	}
	for len(s.items) >= 8 || s.bytes+len(pcm) > 24<<20 {
		for k, v := range s.items {
			if v.timer != nil {
				v.timer.Stop()
			}
			s.bytes -= len(v.pcm)
			delete(s.items, k)
			break
		}
	}
	expires := time.Now().Add(15 * time.Minute)
	s.bytes += len(pcm)
	s.items[id] = audioItem{pcm: append([]byte(nil), pcm...), expires: expires, timer: time.AfterFunc(15*time.Minute, func() { s.removeExpired(id, expires) })}
	return true
}
func (s *ephemeralAudioStore) removeExpired(id string, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, ok := s.items[id]; ok && item.expires.Equal(expires) {
		s.bytes -= len(item.pcm)
		delete(s.items, id)
	}
}
func (s *ephemeralAudioStore) cleanup() {
	now := time.Now()
	for k, v := range s.items {
		if !now.Before(v.expires) {
			if v.timer != nil {
				v.timer.Stop()
			}
			s.bytes -= len(v.pcm)
			delete(s.items, k)
		}
	}
}
func (s *ephemeralAudioStore) get(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	v, ok := s.items[id]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), v.pcm...), true
}
func wav24k(pcm []byte) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], 1)
	binary.LittleEndian.PutUint32(out[24:], 24000)
	binary.LittleEndian.PutUint32(out[28:], 48000)
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}
