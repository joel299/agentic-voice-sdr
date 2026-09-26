package agentprompt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	domain "github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
)

var (
	// ErrDatabaseOperation is returned for unexpected database failures. The
	// underlying driver error is intentionally not exposed because PostgreSQL
	// errors can contain row values, including prompt content.
	ErrDatabaseOperation = errors.New("database operation failed")
	ErrInvalidStoredData = errors.New("stored prompt violates domain contract")
	errNilPool           = errors.New("postgres prompt repository requires a pool")
)

// Repository persists immutable Agent Prompt versions in PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

var _ domain.PromptRepository = (*Repository)(nil)

// NewRepository constructs a repository around an already-open pgx pool.
func NewRepository(pool *pgxpool.Pool) (*Repository, error) {
	if pool == nil {
		return nil, errNilPool
	}
	return &Repository{pool: pool}, nil
}

const (
	activationLockNamespace int32 = 0x475255 // "GRU"
	activationLockKey       int32 = 132
)

const promptColumns = `version, name, prompt, is_active, created_at, activated_at`

func (r *Repository) CreateVersion(ctx context.Context, draft domain.PromptDraft) (domain.PromptVersion, error) {
	if err := contextError(ctx); err != nil {
		return domain.PromptVersion{}, err
	}
	if err := draft.Validate(); err != nil {
		return domain.PromptVersion{}, domain.ErrInvalidPrompt
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO agent_prompt_versions (name, prompt)
		VALUES ($1, $2)
		RETURNING `+promptColumns,
		strings.TrimSpace(draft.Name), strings.TrimSpace(draft.Content),
	)
	version, err := scanPromptVersion(row)
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "create_version", err)
	}
	return version, nil
}

func (r *Repository) GetActive(ctx context.Context) (domain.PromptVersion, error) {
	if err := contextError(ctx); err != nil {
		return domain.PromptVersion{}, err
	}
	row := r.pool.QueryRow(ctx, `SELECT `+promptColumns+`
		FROM agent_prompt_versions WHERE is_active = TRUE`)
	version, err := scanPromptVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PromptVersion{}, domain.ErrNoActivePrompt
	}
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "get_active", err)
	}
	return version, nil
}

func (r *Repository) GetVersion(ctx context.Context, number int) (domain.PromptVersion, error) {
	if err := contextError(ctx); err != nil {
		return domain.PromptVersion{}, err
	}
	if number <= 0 {
		return domain.PromptVersion{}, domain.ErrPromptNotFound
	}
	version, err := scanPromptVersion(r.pool.QueryRow(ctx,
		`SELECT `+promptColumns+` FROM agent_prompt_versions WHERE version = $1`, number))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PromptVersion{}, domain.ErrPromptNotFound
	}
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "get_version", err)
	}
	return version, nil
}

func (r *Repository) ListVersions(ctx context.Context) ([]domain.PromptVersion, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+promptColumns+` FROM agent_prompt_versions ORDER BY version DESC`)
	if err != nil {
		return nil, r.operationError(ctx, "list_versions", err)
	}
	defer rows.Close()

	versions := make([]domain.PromptVersion, 0)
	for rows.Next() {
		version, scanErr := scanPromptVersion(rows)
		if scanErr != nil {
			return nil, r.operationError(ctx, "list_versions", scanErr)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, r.operationError(ctx, "list_versions", err)
	}
	return versions, nil
}

func (r *Repository) ActivateVersion(ctx context.Context, number int) (domain.PromptVersion, error) {
	if err := contextError(ctx); err != nil {
		return domain.PromptVersion{}, err
	}
	if number <= 0 {
		return domain.PromptVersion{}, domain.ErrPromptNotFound
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "activate_version", err)
	}
	defer rollback(tx)

	if err := lockActivation(ctx, tx); err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "activate_version", err)
	}
	target, err := scanPromptVersion(tx.QueryRow(ctx,
		`SELECT `+promptColumns+` FROM agent_prompt_versions WHERE version = $1 FOR UPDATE`, number))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PromptVersion{}, domain.ErrPromptNotFound
	}
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "activate_version", err)
	}
	if target.Active() {
		if err := tx.Commit(ctx); err != nil {
			return domain.PromptVersion{}, r.operationError(ctx, "activate_version", err)
		}
		return target, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_prompt_versions SET is_active = FALSE WHERE is_active = TRUE`); err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "activate_version", err)
	}
	target, err = scanPromptVersion(tx.QueryRow(ctx,
		`UPDATE agent_prompt_versions
		 SET is_active = TRUE, activated_at = now()
		 WHERE version = $1
		 RETURNING `+promptColumns, number))
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "activate_version", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "activate_version", err)
	}
	return target, nil
}

// CreateAndActivate creates and activates a new version in one transaction.
// It intentionally remains a concrete PostgreSQL capability, not part of the
// domain PromptRepository interface.
func (r *Repository) CreateAndActivate(ctx context.Context, draft domain.PromptDraft) (domain.PromptVersion, error) {
	if err := contextError(ctx); err != nil {
		return domain.PromptVersion{}, err
	}
	if err := draft.Validate(); err != nil {
		return domain.PromptVersion{}, domain.ErrInvalidPrompt
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "create_and_activate", err)
	}
	defer rollback(tx)

	if err := lockActivation(ctx, tx); err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "create_and_activate", err)
	}
	created, err := scanPromptVersion(tx.QueryRow(ctx,
		`INSERT INTO agent_prompt_versions (name, prompt)
		 VALUES ($1, $2)
		 RETURNING `+promptColumns,
		strings.TrimSpace(draft.Name), strings.TrimSpace(draft.Content)))
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "create_and_activate", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_prompt_versions SET is_active = FALSE WHERE is_active = TRUE`); err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "create_and_activate", err)
	}
	activated, err := scanPromptVersion(tx.QueryRow(ctx,
		`UPDATE agent_prompt_versions
		 SET is_active = TRUE, activated_at = now()
		 WHERE version = $1
		 RETURNING `+promptColumns, created.Version()))
	if err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "create_and_activate", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.PromptVersion{}, r.operationError(ctx, "create_and_activate", err)
	}
	return activated, nil
}

func lockActivation(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, activationLockNamespace, activationLockKey)
	return err
}

func rollback(tx pgx.Tx) {
	_ = tx.Rollback(context.Background())
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return ctx.Err()
}

func (r *Repository) operationError(ctx context.Context, operation string, err error) error {
	if errors.Is(err, ErrInvalidStoredData) {
		return fmt.Errorf("postgres agent prompt %s: %w", operation, ErrInvalidStoredData)
	}
	if errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled {
		return fmt.Errorf("postgres agent prompt %s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("postgres agent prompt %s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("postgres agent prompt %s: %w", operation, ErrDatabaseOperation)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPromptVersion(row rowScanner) (domain.PromptVersion, error) {
	var (
		number      int64
		name        string
		content     string
		active      bool
		createdAt   time.Time
		activatedAt pgtype.Timestamptz
	)
	if err := row.Scan(&number, &name, &content, &active, &createdAt, &activatedAt); err != nil {
		return domain.PromptVersion{}, err
	}
	if number <= 0 || number > int64(int(^uint(0)>>1)) {
		return domain.PromptVersion{}, ErrInvalidStoredData
	}
	var activated time.Time
	if activatedAt.Valid {
		activated = activatedAt.Time
	}
	version, err := domain.NewPromptVersion(int(number), name, content, active, createdAt, activated)
	if err != nil {
		return domain.PromptVersion{}, ErrInvalidStoredData
	}
	return version, nil
}
