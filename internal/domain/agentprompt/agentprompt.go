package agentprompt

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxPromptNameRunes    = 120
	maxPromptContentBytes = 32768
)

var (
	ErrInvalidPrompt  = errors.New("invalid prompt")
	ErrPromptNotFound = errors.New("prompt version not found")
	ErrNoActivePrompt = errors.New("no active prompt")
)

// PromptDraft is editable input for creating a new immutable prompt version.
type PromptDraft struct {
	Name    string
	Content string
}

func (d PromptDraft) Validate() error {
	name := strings.TrimSpace(d.Name)
	content := strings.TrimSpace(d.Content)
	if name == "" || utf8.RuneCountInString(name) > maxPromptNameRunes {
		return ErrInvalidPrompt
	}
	if content == "" || len([]byte(content)) > maxPromptContentBytes {
		return ErrInvalidPrompt
	}
	return nil
}

func (d PromptDraft) normalized() (PromptDraft, error) {
	normalized := PromptDraft{
		Name:    strings.TrimSpace(d.Name),
		Content: strings.TrimSpace(d.Content),
	}
	if err := normalized.Validate(); err != nil {
		return PromptDraft{}, err
	}
	return normalized, nil
}

// PromptVersion is an immutable persisted prompt version.
type PromptVersion struct {
	version     int
	name        string
	content     string
	active      bool
	createdAt   time.Time
	activatedAt time.Time
}

func NewPromptVersion(version int, name, content string, active bool, createdAt, activatedAt time.Time) (PromptVersion, error) {
	draft, err := (PromptDraft{Name: name, Content: content}).normalized()
	if err != nil || version <= 0 {
		return PromptVersion{}, ErrInvalidPrompt
	}
	if active && activatedAt.IsZero() {
		return PromptVersion{}, ErrInvalidPrompt
	}
	return PromptVersion{
		version:     version,
		name:        draft.Name,
		content:     draft.Content,
		active:      active,
		createdAt:   createdAt,
		activatedAt: activatedAt,
	}, nil
}

func (p PromptVersion) Validate() error {
	_, err := NewPromptVersion(p.version, p.name, p.content, p.active, p.createdAt, p.activatedAt)
	return err
}

func (p PromptVersion) Version() int         { return p.version }
func (p PromptVersion) Name() string         { return p.name }
func (p PromptVersion) Content() string      { return p.content }
func (p PromptVersion) Active() bool         { return p.active }
func (p PromptVersion) CreatedAt() time.Time { return p.createdAt }

// ActivatedAt returns the activation timestamp and whether this version has
// ever been activated.
func (p PromptVersion) ActivatedAt() (time.Time, bool) {
	return p.activatedAt, !p.activatedAt.IsZero()
}

// Snapshot freezes the content and identity for a new session.
func (p PromptVersion) Snapshot() PromptSnapshot {
	return PromptSnapshot{
		version: p.version,
		name:    p.name,
		content: p.content,
	}
}

// PromptSnapshot is the immutable prompt captured by a session. It has no
// active flag because activation is global state, not session state.
type PromptSnapshot struct {
	version int
	name    string
	content string
}

func (p PromptSnapshot) Version() int    { return p.version }
func (p PromptSnapshot) Name() string    { return p.name }
func (p PromptSnapshot) Content() string { return p.content }

// PromptRepository is the provider-agnostic persistence boundary for prompt
// versions. Implementations own transactional activation and history rules.
type PromptRepository interface {
	CreateVersion(ctx context.Context, draft PromptDraft) (PromptVersion, error)
	GetActive(ctx context.Context) (PromptVersion, error)
	GetVersion(ctx context.Context, version int) (PromptVersion, error)
	ListVersions(ctx context.Context) ([]PromptVersion, error)
	ActivateVersion(ctx context.Context, version int) (PromptVersion, error)
}

// PromptService applies domain validation and exposes version/snapshot
// operations without leaking storage, HTTP, or provider concepts.
type PromptService struct {
	repository PromptRepository
}

func NewPromptService(repository PromptRepository) (*PromptService, error) {
	if repository == nil {
		return nil, ErrInvalidPrompt
	}
	return &PromptService{repository: repository}, nil
}

func (s *PromptService) CreateVersion(ctx context.Context, draft PromptDraft) (PromptVersion, error) {
	normalized, err := draft.normalized()
	if err != nil {
		return PromptVersion{}, err
	}
	return s.repository.CreateVersion(ctx, normalized)
}

func (s *PromptService) GetActive(ctx context.Context) (PromptVersion, error) {
	return s.repository.GetActive(ctx)
}

func (s *PromptService) GetVersion(ctx context.Context, version int) (PromptVersion, error) {
	return s.repository.GetVersion(ctx, version)
}

func (s *PromptService) ListVersions(ctx context.Context) ([]PromptVersion, error) {
	return s.repository.ListVersions(ctx)
}

func (s *PromptService) ActivateVersion(ctx context.Context, version int) (PromptVersion, error) {
	return s.repository.ActivateVersion(ctx, version)
}

func (s *PromptService) SnapshotActive(ctx context.Context) (PromptSnapshot, error) {
	version, err := s.GetActive(ctx)
	if err != nil {
		return PromptSnapshot{}, err
	}
	return version.Snapshot(), nil
}
