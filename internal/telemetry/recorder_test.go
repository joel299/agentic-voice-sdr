package telemetry

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanonicalMetrics(t *testing.T) {
	want := []Metric{
		MetricGeminiConnect,
		MetricSetupAck,
		MetricFirstInputTranscription,
		MetricFirstOutputTranscription,
		MetricFirstAudio,
		MetricTurnLatency,
		MetricInterruptionLatency,
		MetricToolCallDetect,
	}
	got := Metrics()
	if len(got) != len(want) {
		t.Fatalf("Metrics() returned %d names, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Metrics()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRecorderMeasuresAndDrainsDuration(t *testing.T) {
	r := NewRecorder(2)
	start := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	end := start.Add(1250 * time.Microsecond)
	if !r.Record(MetricTurnLatency, start, end) {
		t.Fatal("Record() rejected a valid measurement")
	}
	got := r.Drain()
	if len(got) != 1 {
		t.Fatalf("Drain() returned %d samples, want 1", len(got))
	}
	if got[0].Metric != MetricTurnLatency || got[0].Duration != 1.25 {
		t.Errorf("sample = %#v, want turn latency of 1.25ms", got[0])
	}
}

func TestRecorderRejectsNegativeDuration(t *testing.T) {
	r := NewRecorder(1)
	start := time.Now()
	if r.Record(MetricFirstAudio, start, start.Add(-time.Millisecond)) {
		t.Fatal("Record() accepted a negative duration")
	}
	if got := r.Drain(); len(got) != 0 {
		t.Fatalf("negative measurement was recorded: %#v", got)
	}
}

func TestTimerRecordsOnlyFirstFinish(t *testing.T) {
	r := NewRecorder(2)
	start := time.Now()
	timer := StartAt(MetricGeminiConnect, start, r)
	if !timer.Finish(start.Add(4 * time.Millisecond)) {
		t.Fatal("first Finish() should record")
	}
	if timer.Finish(start.Add(8 * time.Millisecond)) {
		t.Fatal("second Finish() should not record")
	}
	got := r.Drain()
	if len(got) != 1 || got[0].Duration != 4 {
		t.Fatalf("samples = %#v, want one 4ms sample", got)
	}
}

func TestRecorderIsConcurrentSafeAndNonBlockingWhenFull(t *testing.T) {
	const workers, perWorker, capacity = 8, 100, 7
	r := NewRecorder(capacity)
	start := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				r.Record(MetricSetupAck, start, start.Add(time.Millisecond))
			}
		}()
	}
	wg.Wait()
	got := r.Drain()
	if len(got) > capacity {
		t.Fatalf("queue retained %d samples, capacity is %d", len(got), capacity)
	}
	if dropped := r.Dropped(); dropped != uint64(workers*perWorker-len(got)) {
		t.Fatalf("Dropped() = %d, want %d", dropped, uint64(workers*perWorker-len(got)))
	}
}

func TestRecorderRejectsUnknownMetricAndCountsFullQueue(t *testing.T) {
	r := NewRecorder(1)
	start := time.Now()
	end := start.Add(time.Millisecond)
	if r.Record(Metric("untrusted_metric"), start, end) {
		t.Fatal("Record() accepted a non-canonical metric")
	}
	if !r.Record(MetricFirstAudio, start, end) {
		t.Fatal("Record() rejected a valid sample")
	}
	if r.Record(MetricFirstAudio, start, end) {
		t.Fatal("Record() blocked/accepted a sample when the queue was full")
	}
	if got := r.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want 1", got)
	}
}

func TestTimerConcurrentFinishRecordsOnce(t *testing.T) {
	const callers = 24
	r := NewRecorder(callers)
	start := time.Now()
	timer := StartAt(MetricTurnLatency, start, r)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if timer.Finish(start.Add(time.Millisecond)) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("successful Finish() calls = %d, want 1", got)
	}
	if got := len(r.Drain()); got != 1 {
		t.Fatalf("recorded samples = %d, want 1", got)
	}
}

func TestDrainIsBoundedToInitialQueueDepth(t *testing.T) {
	r := NewRecorder(3)
	start := time.Now()
	for _, duration := range []time.Duration{time.Millisecond, 2 * time.Millisecond} {
		if !r.Record(MetricTurnLatency, start, start.Add(duration)) {
			t.Fatal("failed to queue initial observation")
		}
	}

	initialDepth := len(r.queue)
	if initialDepth != 2 {
		t.Fatalf("initial queue depth = %d, want 2", initialDepth)
	}
	if !r.Record(MetricFirstAudio, start, start.Add(3*time.Millisecond)) {
		t.Fatal("failed to queue observation added after the snapshot")
	}

	got := drainQueue(r.queue, initialDepth)
	if len(got) > initialDepth {
		t.Fatalf("drain returned %d observations; initial queue depth was %d", len(got), initialDepth)
	}
	if len(got) != initialDepth {
		t.Fatalf("drain returned %d initial observations, want %d", len(got), initialDepth)
	}
	if remaining := len(r.queue); remaining != 1 {
		t.Fatalf("queue has %d observations after drain, want the post-snapshot observation to remain", remaining)
	}
	if next := r.Drain(); len(next) != 1 || next[0].Metric != MetricFirstAudio {
		t.Fatalf("next drain = %#v, want the post-snapshot observation", next)
	}
}
