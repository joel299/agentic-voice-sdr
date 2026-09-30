package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/callservice"
)

// OutboundCallService is the narrow HTTP-facing call control contract.
type OutboundCallService interface {
	Start(context.Context, string) (callservice.Call, error)
	Get(string) (callservice.Call, error)
	Hangup(context.Context, string) (callservice.Call, error)
}

type callAPI struct{ service OutboundCallService }

func registerCallRoutes(router chi.Router, service OutboundCallService, authorizer OwnerAuthorizer) {
	a := &callAPI{service: service}
	router.Route("/v1/calls", func(r chi.Router) {
		r.Use(ownerOnly(authorizer))
		r.Post("/", a.create)
		r.Get("/{call_id}", a.get)
		r.Post("/{call_id}/hangup", a.hangup)
	})
}

func ownerOnly(authorizer OwnerAuthorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isNilInterface(authorizer) || !authorizer.Authorize(r) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *callAPI) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		To string `json:"to"`
	}
	if decodeJSON(w, r, &input) != nil {
		return
	}
	if a.service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "call control unavailable"})
		return
	}
	call, err := a.service.Start(r.Context(), input.To)
	if err != nil {
		writeCallError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, call)
}

func (a *callAPI) get(w http.ResponseWriter, r *http.Request) {
	if a.service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "call control unavailable"})
		return
	}
	call, err := a.service.Get(chi.URLParam(r, "call_id"))
	if err != nil {
		writeCallError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, call)
}

func (a *callAPI) hangup(w http.ResponseWriter, r *http.Request) {
	if a.service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "call control unavailable"})
		return
	}
	call, err := a.service.Hangup(r.Context(), chi.URLParam(r, "call_id"))
	if err != nil {
		writeCallError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, call)
}

func writeCallError(w http.ResponseWriter, err error) {
	status, message := http.StatusBadGateway, "call control request failed"
	switch {
	case errors.Is(err, callservice.ErrInvalidDestination):
		status, message = http.StatusBadRequest, "invalid destination"
	case errors.Is(err, callservice.ErrDestinationDenied):
		status, message = http.StatusForbidden, "destination is not allowed"
	case errors.Is(err, callservice.ErrCallNotFound):
		status, message = http.StatusNotFound, "call not found"
	case errors.Is(err, callservice.ErrCallActive):
		status, message = http.StatusConflict, "an active call already exists"
	case errors.Is(err, callservice.ErrNotRegistered):
		status, message = http.StatusConflict, "telephony provider is not registered"
	case errors.Is(err, callservice.ErrCallNotActive):
		status, message = http.StatusConflict, "call is not active"
	case errors.Is(err, callservice.ErrHangupRequested):
		status, message = http.StatusConflict, "hangup already requested"
	case errors.Is(err, callservice.ErrProviderFailure):
		status, message = http.StatusBadGateway, "telephony provider request failed"
	}
	writeJSON(w, status, map[string]string{"error": message})
}
