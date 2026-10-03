package voicecall_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	domain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	repository "github.com/joel299/agentic-voice-sdr/internal/integrations/postgres/voicecall"
)

func TestPostgresVoiceCallRepositoryContract(t *testing.T) {
	for _, key := range []string{"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE"} {
		if os.Getenv(key) == "" {
			t.Skip("PostgreSQL integration variables are not configured")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminCfg, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal("parse PG config")
	}
	admin, err := pgxpool.NewWithConfig(ctx, adminCfg)
	if err != nil {
		t.Fatal("connect to PostgreSQL")
	}
	defer admin.Close()
	schema := fmt.Sprintf("gru154_repo_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal("create test schema")
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if _, e := admin.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Error("drop isolated test schema")
		}
	}()
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal("parse isolated config")
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("connect isolated pool")
	}
	defer pool.Close()
	var major int
	if err := pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int / 10000").Scan(&major); err != nil || major != 16 {
		t.Fatal("contract test requires PostgreSQL 16")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration")
	}
	migration, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../../../db/migrations/0005_voice_calls_and_transcript_turns.sql"))
	if err != nil {
		t.Fatal("read migration")
	}
	if _, err = pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal("apply real migration")
	}
	aiMigration, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../../../db/migrations/0006_call_ai_runtime_status.sql"))
	if err != nil {
		t.Fatal("read AI runtime migration")
	}
	if _, err = pool.Exec(ctx, string(aiMigration)); err != nil {
		t.Fatal("apply AI runtime migration")
	}
	stageMigration, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../../../db/migrations/0007_call_ai_runtime_stage.sql"))
	if err != nil {
		t.Fatal("read AI stage migration")
	}
	if _, err = pool.Exec(ctx, string(stageMigration)); err != nil {
		t.Fatal("apply AI stage migration")
	}
	unknownMigration, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../../../db/migrations/0008_unknown_ai_runtime_stage.sql"))
	if err != nil {
		t.Fatal("read unknown stage migration")
	}
	if _, err = pool.Exec(ctx, string(unknownMigration)); err != nil {
		t.Fatal("apply unknown stage migration")
	}
	repo, err := repository.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	call := domain.Call{ID: "call-gru154", Destination: "+15551234567", Status: "dialing", Provider: "baresip"}
	if err := repo.CreateCall(ctx, call); err != nil {
		t.Fatalf("create call: %v", err)
	}
	if err := repo.UpdateLifecycle(ctx, call.ID, "ringing", "provider-1", ""); err != nil {
		t.Fatalf("ringing update: %v", err)
	}
	if err := repo.UpdateLifecycle(ctx, call.ID, "connected", "provider-1", ""); err != nil {
		t.Fatalf("connected update: %v", err)
	}
	failureAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.UpdateAIRuntimeStatus(ctx, call.ID, "failed", "input_transcription_receive", "provider_transport", &failureAt); err != nil {
		t.Fatalf("AI runtime update: %v", err)
	}
	// Call #4 used input_transcription_handler as a failure class, which the
	// schema rejects. The corrected runtime uses runtime_error plus that stage.
	if err := repo.UpdateAIRuntimeStatus(ctx, call.ID, "failed", "input_transcription_handler", "runtime_error", &failureAt); err != nil {
		t.Fatalf("handler failure persistence: %v", err)
	}
	if err := repo.UpdateAIRuntimeStatus(ctx, call.ID, "failed", "runtime_unknown", "runtime_unknown", &failureAt); err != nil {
		t.Fatalf("unknown failure persistence: %v", err)
	}
	if err := repo.UpdateAIRuntimeStatus(ctx, call.ID, "failed", "input_transcription_receive", "provider_transport", &failureAt); err != nil {
		t.Fatal(err)
	}
	lead, created, err := repo.AppendFinalTurn(ctx, call.ID, "lead", " Olá, quero informações. ", "gemini_input", "lead:"+call.ID+":lead-000001")
	if err != nil || !created {
		t.Fatalf("append lead: created=%v err=%v", created, err)
	}
	agent, created, err := repo.AppendFinalTurn(ctx, call.ID, "agent", "Claro, posso te explicar.", "gemini_output", "agent:"+call.ID+":lead-000001")
	if err != nil || !created {
		t.Fatalf("append agent: created=%v err=%v", created, err)
	}
	if lead.Sequence != 1 || agent.Sequence != 2 {
		t.Fatalf("sequence lead=%d agent=%d", lead.Sequence, agent.Sequence)
	}
	dup, created, err := repo.AppendFinalTurn(ctx, call.ID, "lead", "Olá, quero informações.", "gemini_input", "lead:"+call.ID+":lead-000001")
	if err != nil || created || dup.ID != lead.ID {
		t.Fatalf("idempotency: created=%v err=%v", created, err)
	}
	secondSameText, created, err := repo.AppendFinalTurn(ctx, call.ID, "lead", "Sim", "gemini_input", "lead:"+call.ID+":lead-000002")
	if err != nil || !created || secondSameText.Sequence != 3 {
		t.Fatalf("same text in a distinct logical turn was not retained: %+v created=%v err=%v", secondSameText, created, err)
	}
	if _, _, err := repo.AppendFinalTurn(ctx, "missing-call", "lead", "x", "gemini_input", "e"); err == nil {
		t.Fatal("foreign key/call existence was not enforced")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO voice_call_transcript_turns(call_id,sequence,role,text,transcript_state,source,idempotency_key) VALUES('missing-call',1,'lead','x','final','gemini_input','fk-test')`); err == nil {
		t.Fatal("database foreign key accepted a missing call")
	}
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, e := repo.AppendFinalTurn(ctx, call.ID, "lead", fmt.Sprintf("utterance %d", i), "gemini_input", fmt.Sprintf("lead:%s:concurrent-%d", call.ID, i))
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent append: %v", e)
		}
	}
	limited, err := repo.ListFinalTurns(ctx, call.ID, 2)
	if err != nil || len(limited) != 2 || limited[0].Sequence != 1 || limited[1].Sequence != 2 {
		t.Fatalf("bounded ordered list=%+v err=%v", limited, err)
	}
	turns, err := repo.ListFinalTurns(ctx, call.ID, 100)
	if err != nil {
		t.Fatal("list turns")
	}
	if len(turns) != n+3 {
		t.Fatalf("turn count=%d", len(turns))
	}
	for i, turn := range turns {
		if turn.Sequence != int64(i+1) {
			t.Fatalf("turn sequence at %d=%d", i, turn.Sequence)
		}
	}
	canceledCtx, cancelList := context.WithCancel(ctx)
	cancelList()
	if _, err := repo.ListFinalTurns(canceledCtx, call.ID, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled final-turn query error=%v", err)
	}
	if err := repo.UpdateLifecycle(ctx, call.ID, "completed", "provider-1", "completed"); err != nil {
		t.Fatal("terminal update")
	}
	stored, err := repo.GetCall(ctx, call.ID)
	if err != nil {
		t.Fatal("get call")
	}
	if stored.Status != "completed" || stored.ProviderCallID != "provider-1" || stored.ConnectedAt == nil || stored.EndedAt == nil || stored.TerminalReason != "completed" || stored.AIRuntimeStatus != "failed" || stored.AIRuntimeStage != "input_transcription_receive" || stored.AIFailureClass != "provider_transport" || stored.AIFailureAt == nil || !stored.AIFailureAt.Equal(failureAt) {
		t.Fatalf("stored lifecycle incomplete: %#v", stored)
	}
	retained, err := repo.ListFinalTurns(ctx, call.ID, 100)
	if err != nil || len(retained) != len(turns) {
		t.Fatalf("terminal lifecycle update changed finalized turns: %d, %v", len(retained), err)
	}
	var forbiddenColumns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='voice_call_transcript_turns' AND column_name IN ('audio','pcm','raw_audio','audio_blob')`).Scan(&forbiddenColumns); err != nil || forbiddenColumns != 0 {
		t.Fatalf("raw PCM/audio storage columns=%d err=%v", forbiddenColumns, err)
	}
}
