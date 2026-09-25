// Package telemetry provides a bounded, in-memory boundary for realtime voice
// latency measurements. It deliberately has no exporter or persistence layer.
package telemetry

import (
	"sync/atomic"
	"time"
)

// Metric is a stable metric name in milliseconds.
type Metric string

const (
	MetricGeminiConnect            Metric = "gemini_connect_ms"
	MetricSetupAck                 Metric = "setup_ack_ms"
	MetricFirstInputTranscription  Metric = "first_input_transcription_ms"
	MetricFirstOutputTranscription Metric = "first_output_transcription_ms"
	MetricFirstAudio               Metric = "first_audio_ms"
	MetricTurnLatency              Metric = "turn_latency_ms"
	MetricInterruptionLatency      Metric = "interruption_latency_ms"
	MetricToolCallDetect           Metric = "tool_call_detect_ms"
)

var canonicalMetrics = [...]Metric{
	MetricGeminiConnect,
	MetricSetupAck,
	MetricFirstInputTranscription,
	MetricFirstOutputTranscription,
	MetricFirstAudio,
	MetricTurnLatency,
	MetricInterruptionLatency,
	MetricToolCallDetect,
}

// Metrics returns a copy of the canonical metric names in stable order.
func Metrics() []Metric {
	return append([]Metric(nil), canonicalMetrics[:]...)
}

// Observation contains metadata only. Duration is expressed in milliseconds;
// audio payloads, credentials, and transcript content are never accepted.
type Observation struct {
	Metric   Metric
	Duration float64
}

// Recorder is a bounded queue. Record is non-blocking; when the queue is full,
// the sample is dropped and counted. Drain is intended for a non-realtime
// consumer. No I/O or goroutine is started by this package.
type Recorder struct {
	queue   chan Observation
	dropped atomic.Uint64
}

// NewRecorder creates a bounded recorder. Non-positive capacities are clamped
// to one so recording remains non-blocking and bounded.
func NewRecorder(capacity int) *Recorder {
	if capacity < 1 {
		capacity = 1
	}
	return &Recorder{queue: make(chan Observation, capacity)}
}

// Record queues the elapsed time between start and end. Callers should use
// time.Now timestamps so Sub can use Go's monotonic clock component. It returns
// false for unknown metrics, negative durations, a nil receiver, or a full
// queue. The full-queue case is counted in Dropped.
func (r *Recorder) Record(metric Metric, start, end time.Time) bool {
	if r == nil || !knownMetric(metric) {
		return false
	}
	elapsed := end.Sub(start)
	if elapsed < 0 {
		return false
	}
	observation := Observation{Metric: metric, Duration: float64(elapsed) / float64(time.Millisecond)}
	select {
	case r.queue <- observation:
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

// Drain removes at most the observations queued at entry without waiting.
func (r *Recorder) Drain() []Observation {
	if r == nil {
		return nil
	}
	initialDepth := len(r.queue)
	return drainQueue(r.queue, initialDepth)
}

func drainQueue(queue <-chan Observation, limit int) []Observation {
	observations := make([]Observation, 0, limit)
	for i := 0; i < limit; i++ {
		select {
		case observation := <-queue:
			observations = append(observations, observation)
		default:
			return observations
		}
	}
	return observations
}

// Dropped returns the number of observations discarded because the queue was
// full. Invalid measurements are rejected but are not counted as queue drops.
func (r *Recorder) Dropped() uint64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

// Timer measures one lifecycle interval. Finish is safe to call concurrently;
// at most one caller records a sample.
type Timer struct {
	metric   Metric
	start    time.Time
	recorder *Recorder
	finished atomic.Bool
}

// StartAt creates a timer at start. Use time.Now() in realtime code to retain
// the monotonic clock reading.
func StartAt(metric Metric, start time.Time, recorder *Recorder) *Timer {
	return &Timer{metric: metric, start: start, recorder: recorder}
}

// Finish ends the interval at end and records it. It returns true only when the
// first finish successfully queues the observation.
func (t *Timer) Finish(end time.Time) bool {
	if t == nil || t.recorder == nil || !t.finished.CompareAndSwap(false, true) {
		return false
	}
	return t.recorder.Record(t.metric, t.start, end)
}

func knownMetric(metric Metric) bool {
	for _, candidate := range canonicalMetrics {
		if metric == candidate {
			return true
		}
	}
	return false
}
