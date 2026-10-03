package baresipmedia

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

func TestActualCFlushDiscardsOldAudioAndKeepsTransport(t *testing.T) {
	bin, module := os.Getenv("BARESIP_C_TEST_BIN"), os.Getenv("BARESIP_C_TEST_MODULE")
	if bin == "" || module == "" {
		t.Skip("opt-in actual C module")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, e := New(ctx, Config{ParentDir: shortTempDir(t), BufferFrames: 2, RXBufferFrames: 4, TXSocketBufferBytes: 1024})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	rx, tx := a.SocketPaths()
	peer, e := net.Dial("unix", rx)
	if e != nil {
		t.Fatal(e)
	}
	defer peer.Close()
	command := exec.CommandContext(ctx, bin, module, "1000", "--external", tx, "--inspect")
	command.Env = append(os.Environ(), "GRU151_MEDIA_TIMING_ACK=1", "GRU151_MEDIA_FLUSH=1")
	stdout, e := command.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { command.Process.Kill(); command.Wait() }()
	s, e := a.WaitSession(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	type callback struct {
		sample int
		ns     int64
	}
	observed := make(chan callback, 256)
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			var v callback
			if _, e := fmt.Sscanf(scan.Text(), "callback_sample=%d callback_unix_ns=%d", &v.sample, &v.ns); e == nil {
				select {
				case observed <- v:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); stdout.Close(); <-scanDone }()
	pcm := func(sample byte, frames int) []byte {
		b := make([]byte, 960*frames)
		for i := 0; i < len(b); i += 2 {
			b[i] = sample
		}
		return b
	}
	writerCtx, stopWriter := context.WithCancel(ctx)
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- s.WriteFrameContext(writerCtx, audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcm(17, 16)})
	}()
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("C first frame missing")
	}
	stopWriter()
	<-writerDone
	if e = s.DiscardPendingAudio(ctx); e != nil {
		t.Fatal(e)
	}
	clearedAt := time.Now().UnixNano()
	if e = s.WriteFrameContext(ctx, audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcm(34, 3)}); e != nil {
		t.Fatal(e)
	}
	n := 0
	for n < 3 {
		select {
		case v := <-observed:
			if v.ns < clearedAt {
				continue
			}
			if v.sample != 34 {
				t.Fatalf("old PCM after flush: %d", v.sample)
			}
			n++
		case <-ctx.Done():
			t.Fatal("new C PCM missing after flush")
		}
	}
	if s.IsClosed() {
		t.Fatal("flush closed media")
	}
}
func TestFlushUnavailableIsExplicit(t *testing.T) {
	a, e := New(context.Background(), Config{ParentDir: shortTempDir(t)})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	rx, tx := a.SocketPaths()
	r, w, s := connectPair(t, a, rx, tx)
	defer r.Close()
	defer w.Close()
	if e = s.DiscardPendingAudio(context.Background()); e == nil || !strings.Contains(e.Error(), "unavailable") {
		t.Fatal("missing flush falsely passed")
	}
}
