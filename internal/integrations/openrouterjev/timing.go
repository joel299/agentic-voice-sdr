package openrouterjev

import (
	"context"
	"crypto/tls"
	"github.com/joel299/agentic-voice-sdr/internal/telemetry"
	"net/http/httptrace"
	"sync"
	"time"
)

// RequestTiming contains no URL, provider body, transcript, credential or error.
type RequestTiming = telemetry.RequestNetworkTiming

func requestTiming(ctx context.Context, size int, observe func(RequestTiming)) (context.Context, func()) {
	if observe == nil {
		return ctx, func() {}
	}
	began := time.Now()
	var mu sync.Mutex
	var dns, connect, tlsAt time.Time
	result := RequestTiming{RequestBytes: size}
	elapsed := func(t time.Time) float64 {
		if t.IsZero() {
			return 0
		}
		return float64(time.Since(t)) / float64(time.Millisecond)
	}
	trace := &httptrace.ClientTrace{DNSStart: func(httptrace.DNSStartInfo) { mu.Lock(); dns = time.Now(); mu.Unlock() }, DNSDone: func(httptrace.DNSDoneInfo) { mu.Lock(); result.DNSMS = elapsed(dns); mu.Unlock() }, ConnectStart: func(string, string) { mu.Lock(); connect = time.Now(); mu.Unlock() }, ConnectDone: func(string, string, error) { mu.Lock(); result.ConnectMS = elapsed(connect); mu.Unlock() }, TLSHandshakeStart: func() { mu.Lock(); tlsAt = time.Now(); mu.Unlock() }, TLSHandshakeDone: func(tls.ConnectionState, error) { mu.Lock(); result.TLSMS = elapsed(tlsAt); mu.Unlock() }, GotConn: func(v httptrace.GotConnInfo) { mu.Lock(); result.ConnectionReused = v.Reused; mu.Unlock() }, GotFirstResponseByte: func() { mu.Lock(); result.FirstByteMS = elapsed(began); mu.Unlock() }}
	return httptrace.WithClientTrace(ctx, trace), func() { mu.Lock(); result.TotalMS = elapsed(began); v := result; mu.Unlock(); observe(v) }
}
