package agentprompt_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	domain "github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	repository "github.com/joel299/agentic-voice-sdr/internal/integrations/postgres/agentprompt"
)

var _ domain.PromptRepository = (*repository.Repository)(nil)

func TestPostgresRepositoryContract(t *testing.T) {
	ctx := context.Background()
	repo, pool := newRepository(t)

	if _, err := repo.GetActive(ctx); !errors.Is(err, domain.ErrNoActivePrompt) {
		t.Fatal("empty database must report ErrNoActivePrompt")
	}
	versions, err := repo.ListVersions(ctx)
	if err != nil || versions == nil || len(versions) != 0 {
		t.Fatal("empty ListVersions must return a non-nil empty slice")
	}
	if _, err := repo.CreateVersion(ctx, domain.PromptDraft{Name: " \t ", Content: "invalid"}); !errors.Is(err, domain.ErrInvalidPrompt) {
		t.Fatal("invalid draft must report ErrInvalidPrompt")
	}
	if _, err := repo.CreateAndActivate(ctx, domain.PromptDraft{Name: "invalid", Content: "\n "}); !errors.Is(err, domain.ErrInvalidPrompt) {
		t.Fatal("CreateAndActivate must reject invalid drafts before writing")
	}
	if versionCount(t, pool) != 0 {
		t.Fatal("invalid drafts must not create persisted rows")
	}

	v1 := createVersion(t, repo, "synthetic-v1", "prompt-content-marker-v1")
	if v1.Version() <= 0 || v1.Active() || v1.CreatedAt().IsZero() {
		t.Fatal("CreateVersion must return a generated positive inactive version")
	}
	if activatedAt, wasActivated := v1.ActivatedAt(); wasActivated || !activatedAt.IsZero() {
		t.Fatal("NULL activated_at must map to zero time for an inactive version")
	}
	v2 := createVersion(t, repo, "synthetic-v2", "prompt-content-marker-v2")
	if v2.Version() <= v1.Version() || v2.Active() {
		t.Fatal("versions must increase and newly created rows must be inactive")
	}

	got, err := repo.GetVersion(ctx, v1.Version())
	if err != nil || got.Name() != "synthetic-v1" || got.Content() != "prompt-content-marker-v1" {
		t.Fatal("GetVersion must return the persisted version content")
	}
	if _, err := repo.GetVersion(ctx, 0); !errors.Is(err, domain.ErrPromptNotFound) {
		t.Fatal("non-positive version must report ErrPromptNotFound")
	}
	if _, err := repo.GetVersion(ctx, v2.Version()+1000); !errors.Is(err, domain.ErrPromptNotFound) {
		t.Fatal("unknown version must report ErrPromptNotFound")
	}
	versions, err = repo.ListVersions(ctx)
	if err != nil || len(versions) != 2 || versions[0].Version() != v2.Version() || versions[1].Version() != v1.Version() {
		t.Fatal("ListVersions must return versions in descending order")
	}

	active, err := repo.ActivateVersion(ctx, v1.Version())
	if err != nil || !active.Active() || activeCount(t, pool) != 1 {
		t.Fatal("first activation must succeed and leave exactly one active version")
	}
	activatedAt, exists := active.ActivatedAt()
	if !exists {
		t.Fatal("activation timestamp must be set")
	}
	activeAgain, err := repo.ActivateVersion(ctx, v1.Version())
	if err != nil || !activeAgain.Active() {
		t.Fatal("activating the current version must succeed idempotently")
	}
	activatedAtAgain, exists := activeAgain.ActivatedAt()
	if !exists || !activatedAtAgain.Equal(activatedAt) {
		t.Fatal("idempotent activation must preserve activated_at")
	}

	active, err = repo.ActivateVersion(ctx, v2.Version())
	if err != nil || !active.Active() || active.Version() != v2.Version() || activeCount(t, pool) != 1 {
		t.Fatal("activating a different version must succeed and leave exactly one active version")
	}
	old, err := repo.GetVersion(ctx, v1.Version())
	oldActivatedAt, oldWasActivated := old.ActivatedAt()
	if err != nil || old.Active() || old.Content() != "prompt-content-marker-v1" || !oldWasActivated || !oldActivatedAt.Equal(activatedAt) {
		t.Fatal("deactivated history must retain its activation timestamp and content")
	}

	beforeUnknownActivation := versionCount(t, pool)
	if _, err := repo.ActivateVersion(ctx, v2.Version()+1000); !errors.Is(err, domain.ErrPromptNotFound) {
		t.Fatal("unknown activation must report ErrPromptNotFound")
	}
	if versionCount(t, pool) != beforeUnknownActivation {
		t.Fatal("unknown activation must not mutate persisted versions")
	}

	createdAndActive, err := repo.CreateAndActivate(ctx, domain.PromptDraft{Name: "synthetic-v3", Content: "prompt-content-marker-v3"})
	if err != nil || !createdAndActive.Active() {
		t.Fatal("CreateAndActivate with an existing active version must succeed")
	}
	old, err = repo.GetVersion(ctx, v2.Version())
	current, currentErr := repo.GetActive(ctx)
	if err != nil || currentErr != nil || old.Active() || current.Version() != createdAndActive.Version() || current.Content() != "prompt-content-marker-v3" {
		t.Fatal("CreateAndActivate must atomically replace the active version")
	}

	beforeFailureCount := versionCount(t, pool)
	installFailActivationTrigger(t, pool)
	failureDraft := domain.PromptDraft{Name: "synthetic-failure", Content: "sensitive-prompt-content-marker"}
	_, failureErr := repo.CreateAndActivate(ctx, failureDraft)
	removeFailActivationTrigger(t, pool)
	if failureErr == nil || !errors.Is(failureErr, repository.ErrDatabaseOperation) {
		t.Fatal("forced activation failure must return a sanitized operational error")
	}
	for _, secret := range []string{failureDraft.Content, os.Getenv("PGPASSWORD"), "postgres://", "postgresql://"} {
		if secret != "" && strings.Contains(failureErr.Error(), secret) {
			t.Fatal("repository error exposed sensitive content or connection material")
		}
	}
	if versionCount(t, pool) != beforeFailureCount {
		t.Fatal("failed CreateAndActivate must roll back the inserted version")
	}
	current, err = repo.GetActive(ctx)
	if err != nil || current.Version() != createdAndActive.Version() {
		t.Fatal("failed CreateAndActivate must preserve the previous active version")
	}

	beforeCanceledCount := versionCount(t, pool)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.CreateVersion(canceled, domain.PromptDraft{Name: "cancelled", Content: "synthetic"}); !errors.Is(err, context.Canceled) {
		t.Fatal("CreateVersion must preserve context cancellation")
	}
	if _, err := repo.CreateAndActivate(canceled, domain.PromptDraft{Name: "cancelled-activate", Content: "synthetic"}); !errors.Is(err, context.Canceled) {
		t.Fatal("CreateAndActivate must preserve context cancellation")
	}
	if versionCount(t, pool) != beforeCanceledCount {
		t.Fatal("canceled context must cause zero mutations")
	}

	v4 := createVersion(t, repo, "synthetic-v4", "concurrency-v4")
	v5 := createVersion(t, repo, "synthetic-v5", "concurrency-v5")
	for attempt := 0; attempt < 20; attempt++ {
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wait sync.WaitGroup
		for _, number := range []int{v4.Version(), v5.Version()} {
			wait.Add(1)
			go func(version int) {
				defer wait.Done()
				<-start
				_, activateErr := repo.ActivateVersion(ctx, version)
				errs <- activateErr
			}(number)
		}
		close(start)
		wait.Wait()
		close(errs)
		for activateErr := range errs {
			if activateErr != nil {
				t.Fatal("concurrent activation returned an unexpected error")
			}
		}
		if activeCount(t, pool) != 1 {
			t.Fatal("concurrent activation must leave exactly one active version")
		}
	}
	active, err = repo.GetActive(ctx)
	if err != nil || (!active.Active() || (active.Version() != v4.Version() && active.Version() != v5.Version())) {
		t.Fatal("concurrent activation must leave one target version active")
	}
}

func TestCreateAndActivateOnEmptyPostgres(t *testing.T) {
	ctx := context.Background()
	repo, pool := newRepository(t)
	created, err := repo.CreateAndActivate(ctx, domain.PromptDraft{Name: "synthetic-first", Content: "synthetic-first-prompt"})
	if err != nil || !created.Active() || created.Version() <= 0 {
		t.Fatal("CreateAndActivate on an empty database must create the first active version")
	}
	active, err := repo.GetActive(ctx)
	if err != nil || active.Version() != created.Version() || activeCount(t, pool) != 1 {
		t.Fatal("empty-database CreateAndActivate must leave exactly one active version")
	}
}

func createVersion(t *testing.T, repo *repository.Repository, name, content string) domain.PromptVersion {
	t.Helper()
	version, err := repo.CreateVersion(context.Background(), domain.PromptDraft{Name: name, Content: content})
	if err != nil {
		t.Fatal("CreateVersion failed")
	}
	return version
}

func newRepository(t *testing.T) (*repository.Repository, *pgxpool.Pool) {
	t.Helper()
	for _, key := range []string{"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE"} {
		if os.Getenv(key) == "" {
			t.Skip("PostgreSQL integration variables are not configured")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adminConfig, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal("could not prepare PostgreSQL integration configuration")
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal("could not connect to PostgreSQL integration database")
	}
	t.Cleanup(func() { adminPool.Close() })

	schema := fmt.Sprintf("gru132_repo_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal("could not create isolated PostgreSQL test schema")
	}

	testConfig, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal("could not prepare isolated PostgreSQL pool")
	}
	if testConfig.ConnConfig.RuntimeParams == nil {
		testConfig.ConnConfig.RuntimeParams = make(map[string]string)
	}
	testConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	testConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	testPool, err := pgxpool.NewWithConfig(ctx, testConfig)
	if err != nil {
		t.Fatal("could not open isolated PostgreSQL pool")
	}
	t.Cleanup(func() {
		testPool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); cleanupErr != nil {
			t.Error("could not clean up isolated PostgreSQL test schema")
		}
	})

	versionNumber := 0
	if err := testPool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int / 10000").Scan(&versionNumber); err != nil || versionNumber != 16 {
		t.Fatal("repository integration tests require PostgreSQL 16")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate canonical PostgreSQL migration")
	}
	migrationPath := filepath.Join(filepath.Dir(sourceFile), "../../../../db/migrations/0004_agent_prompt_versions.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal("could not read canonical prompt migration")
	}
	if _, err := testPool.Exec(ctx, string(migration)); err != nil {
		t.Fatal("could not apply canonical prompt migration to isolated schema")
	}
	repo, err := repository.NewRepository(testPool)
	if err != nil {
		t.Fatal("could not construct PostgreSQL prompt repository")
	}
	return repo, testPool
}

func versionCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM agent_prompt_versions").Scan(&count); err != nil {
		t.Fatal("could not count prompt versions")
	}
	return count
}

func activeCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM agent_prompt_versions WHERE is_active").Scan(&count); err != nil {
		t.Fatal("could not count active prompt versions")
	}
	return count
}

func installFailActivationTrigger(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	const sql = `
		CREATE FUNCTION test_fail_prompt_activation() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.is_active AND NOT OLD.is_active THEN
				RAISE EXCEPTION 'synthetic activation failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER test_fail_prompt_activation
		BEFORE UPDATE ON agent_prompt_versions
		FOR EACH ROW EXECUTE FUNCTION test_fail_prompt_activation();`
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal("could not install synthetic rollback trigger")
	}
}

func removeFailActivationTrigger(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DROP TRIGGER test_fail_prompt_activation ON agent_prompt_versions; DROP FUNCTION test_fail_prompt_activation()`); err != nil {
		t.Fatal("could not remove synthetic rollback trigger")
	}
}
