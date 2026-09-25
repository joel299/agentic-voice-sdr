package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
)

type AgentPromptManager interface {
	GetActive(ctx context.Context) (agentprompt.PromptVersion, error)
	ListVersions(ctx context.Context) ([]agentprompt.PromptVersion, error)
	ActivateVersion(ctx context.Context, version int) (agentprompt.PromptVersion, error)
	CreateAndActivate(ctx context.Context, draft agentprompt.PromptDraft) (agentprompt.PromptVersion, error)
}

type agentPromptHandler struct {
	manager AgentPromptManager
}

type agentPromptResponse struct {
	Version     int     `json:"version"`
	Name        string  `json:"name"`
	Prompt      string  `json:"prompt"`
	IsActive    bool    `json:"is_active"`
	CreatedAt   string  `json:"created_at"`
	ActivatedAt *string `json:"activated_at"`
}

type agentPromptPutRequest struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

func NewAgentPromptHandler(manager AgentPromptManager) (http.Handler, error) {
	if manager == nil {
		return nil, errors.New("agent prompt manager is required")
	}
	handler := &agentPromptHandler{manager: manager}
	router := chi.NewRouter()
	router.Get("/api/v1/agent/prompt", handler.getActive)
	router.Put("/api/v1/agent/prompt", handler.putPrompt)
	router.Get("/api/v1/agent/prompt/versions", handler.listVersions)
	router.Post("/api/v1/agent/prompt/versions/{version}/activate", handler.activateVersion)
	return router, nil
}

func (h *agentPromptHandler) getActive(w http.ResponseWriter, r *http.Request) {
	prompt, err := h.manager.GetActive(r.Context())
	if err != nil {
		writeAgentPromptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentPromptResponse(prompt))
}

func (h *agentPromptHandler) putPrompt(w http.ResponseWriter, r *http.Request) {
	var request agentPromptPutRequest
	if decodeJSON(w, r, &request) != nil {
		return
	}
	prompt, err := h.manager.CreateAndActivate(r.Context(), agentprompt.PromptDraft{
		Name: request.Name, Content: request.Prompt,
	})
	if err != nil {
		writeAgentPromptError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAgentPromptResponse(prompt))
}

func (h *agentPromptHandler) listVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := h.manager.ListVersions(r.Context())
	if err != nil {
		writeAgentPromptError(w, err)
		return
	}
	response := make([]agentPromptResponse, 0, len(versions))
	for _, version := range versions {
		response = append(response, toAgentPromptResponse(version))
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": response})
}

func (h *agentPromptHandler) activateVersion(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 0)
	if err != nil || version <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid prompt version"})
		return
	}
	prompt, err := h.manager.ActivateVersion(r.Context(), int(version))
	if err != nil {
		writeAgentPromptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentPromptResponse(prompt))
}

func toAgentPromptResponse(prompt agentprompt.PromptVersion) agentPromptResponse {
	response := agentPromptResponse{
		Version: prompt.Version(), Name: prompt.Name(), Prompt: prompt.Content(),
		IsActive: prompt.Active(), CreatedAt: prompt.CreatedAt().Format(time.RFC3339),
	}
	if activatedAt, ok := prompt.ActivatedAt(); ok {
		formatted := activatedAt.Format(time.RFC3339)
		response.ActivatedAt = &formatted
	}
	return response
}

func writeAgentPromptError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "agent prompt request failed"
	switch {
	case errors.Is(err, agentprompt.ErrNoActivePrompt):
		status, message = http.StatusNotFound, "agent prompt is not configured"
	case errors.Is(err, agentprompt.ErrPromptNotFound):
		status, message = http.StatusNotFound, "agent prompt version not found"
	case errors.Is(err, agentprompt.ErrInvalidPrompt):
		status, message = http.StatusBadRequest, "invalid agent prompt"
	}
	writeJSON(w, status, map[string]string{"error": message})
}
