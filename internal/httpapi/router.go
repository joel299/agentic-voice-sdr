package httpapi

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/whatsapp"
)

//go:embed openapi.yaml static/scalar.html static/scalar.js
var apiDocs embed.FS

type configAPI struct {
	whatsapp  *whatsapp.Service
	sip       SIPConfigurator
	mu        sync.RWMutex
	sipConfig *SIPSafeResponse
}

func NewRouter() http.Handler {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Config{}
	}
	return NewRouterWithConfig(cfg)
}

func NewRouterWithConfig(cfg config.Config) http.Handler {
	return NewRouterWithConfigAndCallService(cfg, nil)
}

func NewRouterWithConfigAndCallService(cfg config.Config, calls OutboundCallService) http.Handler {
	return NewRouterWithConfigCallAndTranscriptServices(cfg, calls, nil)
}

func NewRouterWithConfigCallAndTranscriptServices(cfg config.Config, calls OutboundCallService, transcripts CallTranscriptReader) http.Handler {
	return NewRouterWithConfigAndCalibration(cfg, calls, transcripts, CalibrationServices{})
}

func NewRouterWithConfigAndCalibration(cfg config.Config, calls OutboundCallService, transcripts CallTranscriptReader, calibration CalibrationServices) http.Handler {
	var store whatsapp.ConfigStore = whatsapp.NewMemoryConfigStore()
	if cfg.WhatsAppConfigPath != "" {
		store = &whatsapp.FileConfigStore{Path: cfg.WhatsAppConfigPath}
	}
	var authorizer OwnerAuthorizer
	if cfg.OwnerAPIToken != "" {
		authorizer, _ = NewStaticBearerAuthorizer(cfg.OwnerAPIToken)
	}
	return NewRouterWithCalibration(whatsapp.NewServiceWithStore(whatsapp.NewRegistry(nil), nil, store), configuredSIPConfigurator(cfg), calls, transcripts, authorizer, calibration)
}

func configuredSIPConfigurator(_ config.Config) SIPConfigurator {
	// The local owner runtime is Baresip-only. Keep historical SIP configurator
	// implementations available for explicitly injected legacy tests, but never
	// mount the Asterisk-backed adapter from runtime configuration.
	return unavailableSIPConfigurator{}
}

func NewRouterWithWhatsAppStore(store whatsapp.ConfigStore) http.Handler {
	return NewRouterWithServices(whatsapp.NewServiceWithStore(whatsapp.NewRegistry(nil), nil, store), unavailableSIPConfigurator{})
}

func NewRouterWithServices(whatsappService *whatsapp.Service, sipConfigurator SIPConfigurator) http.Handler {
	return NewRouterWithServicesAndCalls(whatsappService, sipConfigurator, nil, nil)
}

func NewRouterWithServicesAndCalls(whatsappService *whatsapp.Service, sipConfigurator SIPConfigurator, calls OutboundCallService, authorizer OwnerAuthorizer) http.Handler {
	return NewRouterWithServicesCallsAndTranscript(whatsappService, sipConfigurator, calls, nil, authorizer)
}

func NewRouterWithServicesCallsAndTranscript(whatsappService *whatsapp.Service, sipConfigurator SIPConfigurator, calls OutboundCallService, transcripts CallTranscriptReader, authorizer OwnerAuthorizer) http.Handler {
	return NewRouterWithCalibration(whatsappService, sipConfigurator, calls, transcripts, authorizer, CalibrationServices{})
}

func NewRouterWithCalibration(whatsappService *whatsapp.Service, sipConfigurator SIPConfigurator, calls OutboundCallService, transcripts CallTranscriptReader, authorizer OwnerAuthorizer, calibration CalibrationServices) http.Handler {
	a := &configAPI{whatsapp: whatsappService, sip: sipConfigurator}
	router := chi.NewRouter()
	router.Get("/healthz", health)
	router.Get("/readyz", ready)
	router.Get("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		data, _ := apiDocs.ReadFile("openapi.yaml")
		_, _ = w.Write(data)
	})
	router.Get("/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data, _ := apiDocs.ReadFile("static/scalar.html")
		_, _ = w.Write(data)
	})
	router.Get("/scalar.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		data, _ := apiDocs.ReadFile("static/scalar.js")
		_, _ = w.Write(data)
	})
	router.Put("/v1/config/whatsapp", a.putWhatsApp)
	router.Get("/v1/config/whatsapp", a.getWhatsApp)
	router.Get("/v1/config/whatsapp/instances", a.getWhatsAppInstances)
	router.Put("/v1/config/whatsapp/instance", a.putWhatsAppInstance)
	router.Post("/v1/config/whatsapp/test", a.testWhatsApp)
	router.Put("/v1/config/sip-trunk", a.putSIP)
	router.Get("/v1/config/sip-trunk", a.getSIP)
	registerCallRoutes(router, calls, transcripts, authorizer)
	registerCalibrationRoutes(router, authorizer, calibration)
	return router
}
func health(w http.ResponseWriter, _ *http.Request) { writeStatus(w, http.StatusOK, "ok") }
func ready(w http.ResponseWriter, _ *http.Request)  { writeStatus(w, http.StatusOK, "ready") }

func (a *configAPI) putWhatsApp(w http.ResponseWriter, r *http.Request) {
	var input whatsapp.ConfigInput
	if decodeJSON(w, r, &input) != nil {
		return
	}
	result, err := a.whatsapp.Configure(r.Context(), input)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (a *configAPI) getWhatsApp(w http.ResponseWriter, r *http.Request) {
	result, err := a.whatsapp.Get(r.Context())
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (a *configAPI) getWhatsAppInstances(w http.ResponseWriter, r *http.Request) {
	result, err := a.whatsapp.Discover(r.Context())
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": result})
}
func (a *configAPI) putWhatsAppInstance(w http.ResponseWriter, r *http.Request) {
	var input struct {
		InstanceID string `json:"instance_id"`
	}
	if decodeJSON(w, r, &input) != nil {
		return
	}
	result, err := a.whatsapp.SelectInstance(r.Context(), input.InstanceID)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (a *configAPI) testWhatsApp(w http.ResponseWriter, r *http.Request) {
	result, err := a.whatsapp.Test(r.Context())
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": result})
}

func (a *configAPI) putSIP(w http.ResponseWriter, r *http.Request) {
	var input SIPConfigRequest
	if decodeJSON(w, r, &input) != nil {
		return
	}
	if err := input.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := a.sip.Configure(r.Context(), input); err != nil {
		if errors.Is(err, errSIPCanonicalValidation) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid SIP configuration"})
			return
		}
		if errors.Is(err, errSIPBoundaryUnavailable) {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "SIP operational boundary unavailable"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "SIP configuration failed"})
		return
	}
	safe := input.SafeView()
	a.mu.Lock()
	a.sipConfig = &safe
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, safe)
}
func (a *configAPI) getSIP(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.sipConfig == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "SIP configuration not found"})
		return
	}
	writeJSON(w, http.StatusOK, *a.sipConfig)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must contain one JSON object"})
		return errors.New("multiple JSON values")
	}
	return nil
}
func writeConfigError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, errSIPCanonicalValidation), errors.Is(err, whatsapp.ErrInvalidConfig), errors.Is(err, whatsapp.ErrNotConfigured), errors.Is(err, whatsapp.ErrNoActiveInstance), errors.Is(err, whatsapp.ErrInstanceNotFound), errors.Is(err, whatsapp.ErrInstanceNotReady):
		status = http.StatusBadRequest
	case errors.Is(err, whatsapp.ErrProviderUnavailable):
		status = http.StatusNotImplemented
	}
	writeJSON(w, status, map[string]string{"error": safeError(err)})
}
func safeError(err error) string {
	switch {
	case errors.Is(err, errSIPCanonicalValidation):
		return "invalid SIP configuration"
	case errors.Is(err, whatsapp.ErrInvalidConfig):
		return "invalid whatsapp configuration"
	case errors.Is(err, whatsapp.ErrProviderOperation):
		return "whatsapp provider request failed"
	case errors.Is(err, whatsapp.ErrInstanceNotFound):
		return "whatsapp instance not found"
	case errors.Is(err, whatsapp.ErrInstanceNotReady):
		return "whatsapp instance is not connected or ready"
	case errors.Is(err, whatsapp.ErrNotConfigured):
		return "whatsapp provider is not configured"
	case errors.Is(err, whatsapp.ErrProviderUnavailable):
		return "whatsapp provider adapter unavailable"
	case errors.Is(err, whatsapp.ErrNoActiveInstance):
		return "no active whatsapp instance"
	}
	return "whatsapp configuration request failed"
}
func writeStatus(w http.ResponseWriter, status int, value string) {
	writeJSON(w, status, map[string]string{"status": value})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
