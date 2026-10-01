package httpapi

import (
	"context"
	"errors"
	"net/http"
)

var ErrFalePacoSIPUnavailable = errors.New("Fale Paco Baresip configuration unavailable")

type FalePacoSIPProfileService interface {
	Get(context.Context) (FalePacoSIPConfig, error)
	ConfigurePassword(context.Context, string) (FalePacoSIPConfig, error)
	ValidateRegistration(context.Context) (FalePacoSIPValidation, error)
}

type FalePacoSIPConfig struct {
	Provider             string          `json:"provider"`
	Host                 string          `json:"host"`
	Port                 int             `json:"port"`
	Transport            string          `json:"transport"`
	Registrar            string          `json:"registrar"`
	OutboundProxy        string          `json:"outbound_proxy"`
	Auth                 FalePacoSIPAuth `json:"auth"`
	FromUser             string          `json:"from_user"`
	FromDomain           string          `json:"from_domain"`
	CallerID             string          `json:"caller_id"`
	RegistrationRequired bool            `json:"registration_required"`
	Enabled              bool            `json:"enabled"`
	PasswordConfigured   bool            `json:"password_configured"`
}

type FalePacoSIPAuth struct {
	Type     string `json:"type"`
	Username string `json:"username"`
	Realm    string `json:"realm"`
}

type FalePacoSIPValidation struct {
	OK                   bool   `json:"ok"`
	PasswordConfigured   bool   `json:"password_configured"`
	CredentialPresent    bool   `json:"credential_present"`
	DomainMatch          bool   `json:"domain_match"`
	UsernameMatch        bool   `json:"username_match"`
	RealmMatch           bool   `json:"realm_match"`
	Transport            string `json:"transport"`
	RegistrationRequired bool   `json:"registration_required"`
	RegistrationState    string `json:"registration_state"`
}

type falePacoSIPAPI struct{ service FalePacoSIPProfileService }

func (a *falePacoSIPAPI) get(w http.ResponseWriter, r *http.Request) {
	result, err := a.service.Get(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Fale Paco Baresip configuration unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *falePacoSIPAPI) put(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Secret string `json:"secret"`
	}
	if decodeJSON(w, r, &input) != nil {
		return
	}
	if input.Secret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "secret is required"})
		return
	}
	result, err := a.service.ConfigurePassword(r.Context(), input.Secret)
	if err != nil {
		if errors.Is(err, ErrInvalidSIPSecret) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "secret is invalid for the Baresip account format"})
			return
		}
		if errors.Is(err, ErrSIPRuntimeBusy) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "SIP settings cannot be changed while a call is active"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Fale Paco Baresip configuration failed"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *falePacoSIPAPI) validate(w http.ResponseWriter, r *http.Request) {
	result, err := a.service.ValidateRegistration(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Baresip registration validation unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

var (
	ErrInvalidSIPSecret = errors.New("invalid SIP secret")
	ErrSIPRuntimeBusy   = errors.New("SIP runtime has an active call")
)

func CanonicalFalePacoSIPForRuntime(passwordConfigured bool) FalePacoSIPConfig {
	return FalePacoSIPConfig{
		Provider: "Fale Paco", Host: "98034.falepaco.com.br", Port: 5060, Transport: "tcp",
		Registrar: "98034.falepaco.com.br", OutboundProxy: "98034.falepaco.com.br:5060",
		Auth:     FalePacoSIPAuth{Type: "userpass", Username: "100", Realm: "98034.falepaco.com.br"},
		FromUser: "100", FromDomain: "98034.falepaco.com.br", CallerID: "551155200455",
		RegistrationRequired: true, Enabled: true, PasswordConfigured: passwordConfigured,
	}
}
