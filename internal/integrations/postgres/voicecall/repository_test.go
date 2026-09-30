package voicecall_test

import (
	"context"
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
	lead, created, err := repo.AppendFinalTurn(ctx, call.ID, "lead", " Olá, quero informações. ", "gemini_input", "lead:event-1")
	if err != nil || !created {
		t.Fatalf("append lead: created=%v err=%v", created, err)
	}
	agent, created, err := repo.AppendFinalTurn(ctx, call.ID, "agent", "Claro, posso te explicar.", "gemini_output", "agent:event-1")
	if err != nil || !created {
		t.Fatalf("append agent: created=%v err=%v", created, err)
	}
	if lead.Sequence != 1 || agent.Sequence != 2 {
		t.Fatalf("sequence lead=%d agent=%d", lead.Sequence, agent.Sequence)
	}
	dup, created, err := repo.AppendFinalTurn(ctx, call.ID, "lead", "Olá, quero informações.", "gemini_input", "lead:event-1")
	if err != nil || created || dup.ID != lead.ID {
		t.Fatalf("idempotency: created=%v err=%v", created, err)
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
			_, _, e := repo.AppendFinalTurn(ctx, call.ID, "lead", fmt.Sprintf("utterance %d", i), "gemini_input", fmt.Sprintf("lead:concurrent-%d", i))
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
	turns, err := repo.ListFinalTurns(ctx, call.ID)
	if err != nil {
		t.Fatal("list turns")
	}
	if len(turns) != n+2 {
		t.Fatalf("turn count=%d", len(turns))
	}
	for i, turn := range turns {
		if turn.Sequence != int64(i+1) {
			t.Fatalf("turn sequence at %d=%d", i, turn.Sequence)
		}
	}
	if err := repo.UpdateLifecycle(ctx, call.ID, "completed", "provider-1", "completed"); err != nil {
		t.Fatal("terminal update")
	}
	stored, err := repo.GetCall(ctx, call.ID)
	if err != nil {
		t.Fatal("get call")
	}
	if stored.Status != "completed" || stored.ProviderCallID != "provider-1" || stored.ConnectedAt == nil || stored.EndedAt == nil || stored.TerminalReason != "completed" {
		t.Fatalf("stored lifecycle incomplete: %#v", stored)
	}
	retained, err := repo.ListFinalTurns(ctx, call.ID)
	if err != nil || len(retained) != len(turns) {
		t.Fatalf("terminal lifecycle update changed finalized turns: %d, %v", len(retained), err)
	}
	var forbiddenColumns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='voice_call_transcript_turns' AND column_name IN ('audio','pcm','raw_audio','audio_blob')`).Scan(&forbiddenColumns); err != nil || forbiddenColumns != 0 {
		t.Fatalf("raw PCM/audio storage columns=%d err=%v", forbiddenColumns, err)
	}
}
