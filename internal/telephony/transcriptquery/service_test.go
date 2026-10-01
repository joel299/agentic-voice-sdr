package transcriptquery

import (
	"context"
	"errors"
	"testing"
	"time"

	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
)

type fakeCallReader struct {
	call voicecalldomain.Call
	err  error
}

func (r fakeCallReader) GetCall(context.Context, string) (voicecalldomain.Call, error) {
	return r.call, r.err
}

type fakeTurnReader struct {
	turns   []voicecalldomain.Turn
	err     error
	limit   int
	callID  string
	respect bool
}

func (r *fakeTurnReader) ListFinalTurns(ctx context.Context, callID string, limit int) ([]voicecalldomain.Turn, error) {
	r.limit, r.callID = limit, callID
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.err != nil {
		return nil, r.err
	}
	turns := append([]voicecalldomain.Turn(nil), r.turns...)
	if r.respect && len(turns) > limit {
		turns = turns[:limit]
	}
	return turns, nil
}

func TestUnknownCallIsDistinctFromExistingEmptyTranscript(t *testing.T) {
	unknown := New(fakeCallReader{err: voicecalldomain.ErrCallNotFound}, &fakeTurnReader{})
	if _, err := unknown.Get(context.Background(), "missing", DefaultLimit); !errors.Is(err, ErrCallNotFound) {
		t.Fatalf("unknown call error=%v", err)
	}
	reader := &fakeTurnReader{}
	query := New(fakeCallReader{call: voicecalldomain.Call{ID: "call-1", Status: "dialing"}}, reader)
	got, err := query.Get(context.Background(), "call-1", 0)
	if err != nil || got.CallID != "call-1" || got.Status != "dialing" || got.Turns == nil || len(got.Turns) != 0 {
		t.Fatalf("existing empty transcript=%+v err=%v", got, err)
	}
	if reader.limit != DefaultLimit {
		t.Fatalf("default limit=%d want %d", reader.limit, DefaultLimit)
	}
}

func TestQueryReturnsOrderedFinalLeadAndAgentOnlyForActiveCall(t *testing.T) {
	now := time.Date(2026, 9, 30, 1, 2, 3, 0, time.UTC)
	reader := &fakeTurnReader{turns: []voicecalldomain.Turn{
		{CallID: "call-1", Sequence: 4, Role: "agent", Text: "final agent", State: "final", CreatedAt: now},
		{CallID: "call-1", Sequence: 2, Role: "lead", Text: "partial", State: "interim", CreatedAt: now},
		{CallID: "call-1", Sequence: 1, Role: "lead", Text: "final lead", State: "final", CreatedAt: now},
		{CallID: "call-1", Sequence: 3, Role: "internal", Text: "hidden", State: "final", CreatedAt: now},
		{CallID: "another-call", Sequence: 5, Role: "lead", Text: "wrong call", State: "final", CreatedAt: now},
	}}
	query := New(fakeCallReader{call: voicecalldomain.Call{ID: "call-1", Status: "connected"}}, reader)
	got, err := query.Get(context.Background(), "call-1", 10)
	if err != nil || got.Status != "connected" || len(got.Turns) != 2 {
		t.Fatalf("active call transcript=%+v err=%v", got, err)
	}
	if got.Turns[0].Sequence != 1 || got.Turns[0].Role != "lead" || got.Turns[1].Sequence != 4 || got.Turns[1].Role != "agent" {
		t.Fatalf("public turns=%+v", got.Turns)
	}
}

func TestQueryBoundsRepositoryLimitAndPropagatesCancellationSafely(t *testing.T) {
	reader := &fakeTurnReader{}
	query := New(fakeCallReader{call: voicecalldomain.Call{ID: "call-1", Status: "completed"}}, reader)
	if _, err := query.Get(context.Background(), "call-1", MaxLimit+1); err != nil {
		t.Fatal(err)
	}
	if reader.limit != MaxLimit {
		t.Fatalf("repository limit=%d want %d", reader.limit, MaxLimit)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := query.Get(ctx, "call-1", 5)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query error=%v", err)
	}
}

func TestQueryMasksRepositoryFailureCategory(t *testing.T) {
	secretErr := errors.New("database password leaked by driver")
	query := New(fakeCallReader{call: voicecalldomain.Call{ID: "call-1", Status: "failed"}}, &fakeTurnReader{err: secretErr})
	_, err := query.Get(context.Background(), "call-1", 10)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, secretErr) {
		t.Fatalf("query failure classification=%v", err)
	}
	query = New(fakeCallReader{err: secretErr}, &fakeTurnReader{})
	_, err = query.Get(context.Background(), "call-1", 10)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, secretErr) {
		t.Fatalf("call lookup failure classification=%v", err)
	}
}
