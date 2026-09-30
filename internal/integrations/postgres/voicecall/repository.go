// Package voicecall implements durable call lifecycle and final transcript storage.
package voicecall

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	domain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
)

var (
	ErrDatabaseOperation = errors.New("database operation failed")
	ErrCallNotFound      = errors.New("call not found")
	ErrInvalidRecord     = errors.New("invalid call or transcript record")
)

type Repository struct{ pool *pgxpool.Pool }

var _ domain.CallRepository = (*Repository)(nil)
var _ domain.TranscriptRepository = (*Repository)(nil)

func NewRepository(pool *pgxpool.Pool) (*Repository, error) {
	if pool == nil {
		return nil, errors.New("postgres voice call repository requires a pool")
	}
	return &Repository{pool: pool}, nil
}

func (r *Repository) CreateCall(ctx context.Context, c domain.Call) error {
	if ctx == nil || c.ID == "" || c.Destination == "" || c.Status == "" || c.Provider == "" {
		return ErrInvalidRecord
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO voice_calls(call_id,destination,status,provider,provider_call_id) VALUES($1,$2,$3,$4,NULLIF($5,''))`, c.ID, c.Destination, c.Status, c.Provider, c.ProviderCallID)
	if err != nil {
		return ErrDatabaseOperation
	}
	return nil
}

func (r *Repository) UpdateLifecycle(ctx context.Context, id, status, providerCallID, reason string) error {
	if ctx == nil || id == "" || status == "" {
		return ErrInvalidRecord
	}
	tag, err := r.pool.Exec(ctx, `UPDATE voice_calls SET status=$2, provider_call_id=COALESCE(NULLIF($3,''),provider_call_id), terminal_reason=CASE WHEN $4 <> '' THEN $4 ELSE terminal_reason END, connected_at=CASE WHEN $2='connected' THEN COALESCE(connected_at,now()) ELSE connected_at END, ended_at=CASE WHEN $2 IN ('completed','busy','no_answer','failed','canceled') THEN COALESCE(ended_at,now()) ELSE ended_at END, updated_at=now() WHERE call_id=$1`, id, status, providerCallID, reason)
	if err != nil {
		return ErrDatabaseOperation
	}
	if tag.RowsAffected() == 0 {
		return ErrCallNotFound
	}
	return nil
}

func (r *Repository) GetCall(ctx context.Context, id string) (domain.Call, error) {
	var c domain.Call
	var providerCallID, terminalReason pgtype.Text
	var connectedAt, endedAt pgtype.Timestamptz
	err := r.pool.QueryRow(ctx, `SELECT call_id,destination,status,provider,provider_call_id,started_at,connected_at,ended_at,terminal_reason,created_at,updated_at FROM voice_calls WHERE call_id=$1`, id).Scan(&c.ID, &c.Destination, &c.Status, &c.Provider, &providerCallID, &c.StartedAt, &connectedAt, &endedAt, &terminalReason, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Call{}, ErrCallNotFound
	}
	if err != nil {
		return domain.Call{}, ErrDatabaseOperation
	}
	if providerCallID.Valid {
		c.ProviderCallID = providerCallID.String
	}
	if connectedAt.Valid {
		t := connectedAt.Time
		c.ConnectedAt = &t
	}
	if endedAt.Valid {
		t := endedAt.Time
		c.EndedAt = &t
	}
	if terminalReason.Valid {
		c.TerminalReason = terminalReason.String
	}
	return c, nil
}

func (r *Repository) AppendFinalTurn(ctx context.Context, callID, role, text, source, idempotencyKey string) (domain.Turn, bool, error) {
	text = strings.TrimSpace(text)
	if ctx == nil || callID == "" || text == "" || idempotencyKey == "" || (role != "lead" && role != "agent") || (source != "gemini_input" && source != "gemini_output") {
		return domain.Turn{}, false, ErrInvalidRecord
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Turn{}, false, ErrDatabaseOperation
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Lock the parent row so sequence allocation is serialized across processes.
	var exists string
	if err = tx.QueryRow(ctx, `SELECT call_id FROM voice_calls WHERE call_id=$1 FOR UPDATE`, callID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return domain.Turn{}, false, ErrCallNotFound
	} else if err != nil {
		return domain.Turn{}, false, ErrDatabaseOperation
	}
	var prior domain.Turn
	err = tx.QueryRow(ctx, `SELECT id,call_id,sequence,role,text,transcript_state,source,idempotency_key,created_at FROM voice_call_transcript_turns WHERE call_id=$1 AND idempotency_key=$2`, callID, idempotencyKey).Scan(&prior.ID, &prior.CallID, &prior.Sequence, &prior.Role, &prior.Text, &prior.State, &prior.Source, &prior.IdempotencyKey, &prior.CreatedAt)
	if err == nil {
		if prior.Role != role || prior.Text != text || prior.Source != source {
			return domain.Turn{}, false, ErrInvalidRecord
		}
		if e := tx.Commit(ctx); e != nil {
			return domain.Turn{}, false, ErrDatabaseOperation
		}
		return prior, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Turn{}, false, ErrDatabaseOperation
	}
	var seq int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM voice_call_transcript_turns WHERE call_id=$1`, callID).Scan(&seq); err != nil {
		return domain.Turn{}, false, ErrDatabaseOperation
	}
	turn := domain.Turn{CallID: callID, Sequence: seq, Role: role, Text: text, State: "final", Source: source, IdempotencyKey: idempotencyKey}
	err = tx.QueryRow(ctx, `INSERT INTO voice_call_transcript_turns(call_id,sequence,role,text,transcript_state,source,idempotency_key) VALUES($1,$2,$3,$4,'final',$5,$6) RETURNING id,created_at`, callID, seq, role, text, source, idempotencyKey).Scan(&turn.ID, &turn.CreatedAt)
	if err != nil {
		return domain.Turn{}, false, ErrDatabaseOperation
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Turn{}, false, ErrDatabaseOperation
	}
	return turn, true, nil
}

func (r *Repository) ListFinalTurns(ctx context.Context, callID string) ([]domain.Turn, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,call_id,sequence,role,text,transcript_state,source,idempotency_key,created_at FROM voice_call_transcript_turns WHERE call_id=$1 AND transcript_state='final' ORDER BY sequence`, callID)
	if err != nil {
		return nil, ErrDatabaseOperation
	}
	defer rows.Close()
	turns := make([]domain.Turn, 0)
	for rows.Next() {
		var t domain.Turn
		if err := rows.Scan(&t.ID, &t.CallID, &t.Sequence, &t.Role, &t.Text, &t.State, &t.Source, &t.IdempotencyKey, &t.CreatedAt); err != nil {
			return nil, ErrDatabaseOperation
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrDatabaseOperation
	}
	return turns, nil
}
