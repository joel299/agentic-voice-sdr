package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/transcriptquery"
)

type CallTranscriptResponse struct {
	CallID string                   `json:"call_id"`
	Status string                   `json:"status"`
	Turns  []TranscriptTurnResponse `json:"turns"`
}

type TranscriptTurnResponse struct {
	Sequence  int64     `json:"sequence"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *callAPI) getTranscript(w http.ResponseWriter, r *http.Request) {
	limit := transcriptquery.DefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
			return
		}
		limit = parsed
		if limit > transcriptquery.MaxLimit {
			limit = transcriptquery.MaxLimit
		}
	}
	if a.transcripts == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transcript query failed"})
		return
	}
	result, err := a.transcripts.Get(r.Context(), chi.URLParam(r, "call_id"), limit)
	if errors.Is(err, transcriptquery.ErrCallNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "call not found"})
		return
	}
	if errors.Is(err, transcriptquery.ErrInvalidCall) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid call id"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transcript query failed"})
		return
	}
	response := CallTranscriptResponse{CallID: result.CallID, Status: result.Status, Turns: make([]TranscriptTurnResponse, 0, len(result.Turns))}
	for _, turn := range result.Turns {
		response.Turns = append(response.Turns, TranscriptTurnResponse{
			Sequence:  turn.Sequence,
			Role:      turn.Role,
			Text:      turn.Text,
			CreatedAt: turn.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, response)
}
