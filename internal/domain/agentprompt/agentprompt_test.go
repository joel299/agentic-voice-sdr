package agentprompt

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type promptMemoryRepository struct {
	versions map[int]PromptVersion
	active   int
	next     int
}

func newPromptMemoryRepository() *promptMemoryRepository {
	return &promptMemoryRepository{versions: make(map[int]PromptVersion), next: 1}
}

func (r *promptMemoryRepository) CreateVersion(_ context.Context, draft PromptDraft) (PromptVersion, error) {
	version, err := NewPromptVersion(r.next, draft.Name, draft.Content, false, time.Unix(int64(r.next), 0), time.Time{})
	if err != nil {
		return PromptVersion{}, err
	}
	r.versions[r.next] = version
	r.next++
	return version, nil
}

func (r *promptMemoryRepository) GetActive(_ context.Context) (PromptVersion, error) {
	if r.active == 0 {
		return PromptVersion{}, ErrNoActivePrompt
	}
	return r.versions[r.active], nil
}

func (r *promptMemoryRepository) GetVersion(_ context.Context, version int) (PromptVersion, error) {
	prompt, ok := r.versions[version]
	if !ok {
		return PromptVersion{}, ErrPromptNotFound
	}
	return prompt, nil
}

func (r *promptMemoryRepository) ListVersions(_ context.Context) ([]PromptVersion, error) {
	result := make([]PromptVersion, 0, len(r.versions))
	for i := 1; i < r.next; i++ {
		if prompt, ok := r.versions[i]; ok {
			result = append(result, prompt)
		}
	}
	return result, nil
}

func (r *promptMemoryRepository) ActivateVersion(_ context.Context, version int) (PromptVersion, error) {
	_, ok := r.versions[version]
	if !ok {
		return PromptVersion{}, ErrPromptNotFound
	}
	now := time.Unix(1000+int64(version), 0)
	for number, existing := range r.versions {
		active := number == version
		activatedAt := time.Time{}
		if active {
			activatedAt = now
		}
		updated, err := NewPromptVersion(existing.Version(), existing.Name(), existing.Content(), active, existing.CreatedAt(), activatedAt)
		if err != nil {
			return PromptVersion{}, err
		}
		r.versions[number] = updated
	}
	r.active = version
	return r.versions[version], nil
}

func newPromptService(t *testing.T) (*PromptService, *promptMemoryRepository) {
	t.Helper()
	repo := newPromptMemoryRepository()
	service, err := NewPromptService(repo)
	if err != nil {
		t.Fatalf("NewPromptService: %v", err)
	}
	return service, repo
}

func TestPromptDraftValidation(t *testing.T) {
	tests := []struct {
		name  string
		draft PromptDraft
	}{
		{"blank name", PromptDraft{Name: "", Content: "valid"}},
		{"whitespace name", PromptDraft{Name: " \t\n", Content: "valid"}},
		{"long name", PromptDraft{Name: strings.Repeat("a", 121), Content: "valid"}},
		{"blank content", PromptDraft{Name: "valid", Content: " \t\n"}},
		{"long content", PromptDraft{Name: "valid", Content: strings.Repeat("a", 32769)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.draft.Validate(); !errors.Is(err, ErrInvalidPrompt) {
				t.Fatalf("Validate() = %v, want ErrInvalidPrompt", err)
			}
		})
	}
	if err := (PromptDraft{Name: " Persona ", Content: " Instructions "}).Validate(); err != nil {
		t.Fatalf("valid draft rejected: %v", err)
	}
	if err := (PromptDraft{Name: "é", Content: strings.Repeat("é", 16384)}).Validate(); err != nil {
		t.Fatalf("byte-limit boundary rejected: %v", err)
	}
	if err := (PromptDraft{Name: "é", Content: strings.Repeat("é", 16385)}).Validate(); !errors.Is(err, ErrInvalidPrompt) {
		t.Fatalf("over-byte-limit content = %v", err)
	}
}

func TestPromptVersionValidationAndImmutability(t *testing.T) {
	if _, err := NewPromptVersion(0, "name", "content", false, time.Now(), time.Time{}); !errors.Is(err, ErrInvalidPrompt) {
		t.Fatalf("version zero = %v, want ErrInvalidPrompt", err)
	}
	version, err := NewPromptVersion(1, "name", "content", true, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("valid version rejected: %v", err)
	}
	if version.Version() != 1 || version.Name() != "name" || version.Content() != "content" || !version.Active() {
		t.Fatalf("unexpected version: %+v", version)
	}
	if _, ok := version.ActivatedAt(); !ok {
		t.Fatal("active version has no activation time")
	}
	typ := reflect.TypeOf(version)
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).PkgPath == "" {
			t.Fatalf("PromptVersion exposes mutable field %s", typ.Field(i).Name)
		}
	}
}

func TestPromptServiceNoActiveAndUnknownVersions(t *testing.T) {
	service, _ := newPromptService(t)
	ctx := context.Background()
	if _, err := service.GetActive(ctx); !errors.Is(err, ErrNoActivePrompt) {
		t.Fatalf("GetActive() = %v, want ErrNoActivePrompt", err)
	}
	if _, err := service.GetVersion(ctx, 99); !errors.Is(err, ErrPromptNotFound) {
		t.Fatalf("GetVersion() = %v, want ErrPromptNotFound", err)
	}
	if _, err := service.ActivateVersion(ctx, 99); !errors.Is(err, ErrPromptNotFound) {
		t.Fatalf("ActivateVersion() = %v, want ErrPromptNotFound", err)
	}
}

func TestPromptVersionsAreHistoricalAndSnapshotsAreStable(t *testing.T) {
	service, _ := newPromptService(t)
	ctx := context.Background()
	v1, err := service.CreateVersion(ctx, PromptDraft{Name: "Persona", Content: "version one"})
	if err != nil {
		t.Fatalf("Create v1: %v", err)
	}
	v1, err = service.ActivateVersion(ctx, v1.Version())
	if err != nil {
		t.Fatalf("Activate v1: %v", err)
	}
	snapshot := v1.Snapshot()
	v2, err := service.CreateVersion(ctx, PromptDraft{Name: "Persona", Content: "version two"})
	if err != nil {
		t.Fatalf("Create v2: %v", err)
	}
	if _, err := service.ActivateVersion(ctx, v2.Version()); err != nil {
		t.Fatalf("Activate v2: %v", err)
	}
	active, err := service.GetActive(ctx)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if active.Version() != v2.Version() || active.Content() != "version two" {
		t.Fatalf("active = %+v, want v2", active)
	}
	if snapshot.Version() != v1.Version() || snapshot.Content() != "version one" || snapshot.Name() != v1.Name() {
		t.Fatalf("snapshot changed: %+v", snapshot)
	}
	historical, err := service.GetVersion(ctx, v1.Version())
	if err != nil {
		t.Fatalf("historical GetVersion: %v", err)
	}
	if historical.Content() != "version one" || historical.Version() != v1.Version() {
		t.Fatalf("historical version changed: %+v", historical)
	}
}

func TestPromptActivationIsIdempotent(t *testing.T) {
	service, _ := newPromptService(t)
	ctx := context.Background()
	version, err := service.CreateVersion(ctx, PromptDraft{Name: "Persona", Content: "content"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first, err := service.ActivateVersion(ctx, version.Version())
	if err != nil {
		t.Fatalf("first activation: %v", err)
	}
	second, err := service.ActivateVersion(ctx, version.Version())
	if err != nil {
		t.Fatalf("second activation: %v", err)
	}
	if second.Version() != first.Version() || !second.Active() {
		t.Fatalf("idempotent activation returned %+v", second)
	}
}

func TestPromptSnapshotDoesNotExposeActive(t *testing.T) {
	typ := reflect.TypeOf(PromptSnapshot{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Name == "Active" || strings.EqualFold(typ.Field(i).Name, "active") {
			t.Fatalf("PromptSnapshot stores active state")
		}
	}
}
