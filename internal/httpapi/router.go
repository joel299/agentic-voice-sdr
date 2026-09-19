package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/joel299/agentic-voice-sdr/internal/whatsapp"
)

type configAPI struct {
	whatsapp  *whatsapp.Service
	sip       SIPConfigurator
	mu        sync.RWMutex
	sipConfig *SIPSafeResponse
}

func NewRouter() http.Handler {
	return NewRouterWithServices(whatsapp.NewService(whatsapp.NewRegistry(nil), nil), unavailableSIPConfigurator{})
}

func NewRouterWithServices(whatsappService *whatsapp.Service, sipConfigurator SIPConfigurator) http.Handler {
	a := &configAPI{whatsapp: whatsappService, sip: sipConfigurator}
	router := chi.NewRouter()
	router.Get("/healthz", health)
	router.Get("/readyz", ready)
	router.Put("/v1/config/whatsapp", a.putWhatsApp)
	router.Get("/v1/config/whatsapp", a.getWhatsApp)
	router.Get("/v1/config/whatsapp/instances", a.getWhatsAppInstances)
	router.Put("/v1/config/whatsapp/instance", a.putWhatsAppInstance)
	router.Post("/v1/config/whatsapp/test", a.testWhatsApp)
	router.Put("/v1/config/sip-trunk", a.putSIP)
	router.Get("/v1/config/sip-trunk", a.getSIP)
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
	case errors.Is(err, whatsapp.ErrInvalidConfig), errors.Is(err, whatsapp.ErrNotConfigured), errors.Is(err, whatsapp.ErrNoActiveInstance), errors.Is(err, whatsapp.ErrInstanceNotFound), errors.Is(err, whatsapp.ErrInstanceNotReady):
		status = http.StatusBadRequest
	case errors.Is(err, whatsapp.ErrProviderUnavailable):
		status = http.StatusNotImplemented
	}
	writeJSON(w, status, map[string]string{"error": safeError(err)})
}
func safeError(err error) string {
	switch {
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
