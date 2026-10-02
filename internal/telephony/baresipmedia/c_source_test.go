package baresipmedia

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

// The parser compiled here is the same translation unit linked into the real
// Baresip module. Go socket fixtures alone cannot catch a C framing regression.
func TestCStreamSourceParser(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Fatal("C compiler required for production media parser regression")
	}
	out, err := exec.Command("sh", "module/test-parser.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("actual C parser: %v\n%s", err, out)
	}
	t.Log(string(out))
}

// Opt-in gate uses the production Go rechunker and the real loaded .so's ausrc
// allocator. No controller or SIP account is present in this test.
func TestActualCModuleGeminiBurst(t *testing.T) {
	bin, module := os.Getenv("BARESIP_C_TEST_BIN"), os.Getenv("BARESIP_C_TEST_MODULE")
	if bin == "" || module == "" {
		t.Skip("run test-module.sh and set BARESIP_C_TEST_BIN/BARESIP_C_TEST_MODULE for actual-module gate")
	}
	for _, count := range []int{18, 250} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			a, err := New(ctx, Config{ParentDir: t.TempDir(), BufferFrames: 4})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			rxPath, txPath := a.SocketPaths()
			rx, err := net.Dial("unix", rxPath)
			if err != nil {
				t.Fatal(err)
			}
			defer rx.Close()
			cmd := exec.CommandContext(ctx, bin, module, fmt.Sprint(count), "--external", txPath)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			session, err := a.WaitSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			payload := make([]byte, count*960)
			for n := 0; n < count; n++ {
				for i := 0; i < 480; i++ {
					payload[n*960+i*2] = byte(n + 1)
					payload[n*960+i*2+1] = byte((n + 1) >> 8)
				}
			}
			// SplitBridge bounds individual Go AudioSocket writes; the C stream
			// still receives all 18/250 wire records as a producer burst.
			for len(payload) > 0 {
				n := min(len(payload), 16*960)
				if err = session.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: payload[:n]}); err != nil {
					t.Fatalf("Go PCM24 write: %v", err)
				}
				payload = payload[n:]
			}
			err = cmd.Wait()
			waited = true
			if err != nil {
				t.Fatalf("actual source: %v\n%s", err, output.String())
			}
			if !bytes.Contains(output.Bytes(), []byte("errors=0 corrupt=0")) {
				t.Fatalf("source gate missing: %s", output.String())
			}
			t.Logf("Go PCM24 -> adapter -> real C module: %s", output.String())
		})
	}
}
