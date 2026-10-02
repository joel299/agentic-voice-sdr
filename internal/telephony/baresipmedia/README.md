# Baresip PCM media adapter

This package exposes the existing `bridge.AudioReader`/`AudioWriter` contract
to Baresip 1.1.0 through two private Unix stream sockets. The socket payloads
use the existing AudioSocket frame encoding; they are local IPC, not a second
SIP or RTP implementation.

| Direction | Baresip boundary | Go contract |
| --- | --- | --- |
| remote RTP -> Go | `auplay` callback, after Baresip decode/resample | `TypeSlin16`, PCM S16LE mono 16 kHz |
| Go -> remote RTP | `ausrc` callback, before Baresip resample/encode | variable `TypeSlin24` PCM chunks, S16LE mono 24 kHz |

The Go-to-Baresip adapter rechunks arbitrary valid even-byte PCM chunks into
20 ms frames before sending them to the module: 480 samples / 960 bytes per
frame. Sample order and values are preserved. The TX accumulator holds at most
one partial frame (958 bytes, since PCM samples are two bytes); a final partial
frame is discarded when that call session closes or is canceled.

Configure Baresip's per-direction sample rates explicitly:

```ini
audio_player       gru151_media,/private/path/rx.sock
audio_source       gru151_media,/private/path/tx.sock
auplay_srate       16000
ausrc_srate        24000
auplay_channels    1
ausrc_channels     1
auplay_format      s16
ausrc_format       s16
module             gru151_media.so
```

The `rx.sock` listener receives Baresip playback samples in Go. The `tx.sock`
listener accepts Go PCM to feed Baresip's audio source. `Adapter.New` creates
both stable socket paths inside a mode-0700 temporary directory; each socket
is mode 0600. Context cancellation closes the listeners and removes the private
directory and socket paths. `Adapter.Close` also waits for all workers.

The listeners persist across calls. `Adapter.WaitSession(ctx)` returns a
call-scoped `Session` implementing `bridge.AudioReader` and
`bridge.AudioWriter`. Closing that session disconnects only its RX/TX pair,
clears its queues and partial PCM, and leaves the adapter available for the
next call. Use the returned session with a per-call bridge so bridge cleanup
does not close the long-running adapter. The adapter admits one RX/TX pair at a
time and immediately rejects extra connections while that pair is active.

The Baresip module accepts only S16LE mono at its required rate and rejects
other parameters. Baresip's own audio pipeline selects its codec's sample rate
from SDP and applies the configured resamplers at `auplay_srate` and
`ausrc_srate`. For PCMU/PCMA at 8 kHz, the native Baresip pipeline therefore
does 8 kHz decoded PCM -> 16 kHz player samples on RX, and 24 kHz source PCM ->
8 kHz encoded samples on TX. This module does not decode or encode RTP codecs.

Each call's RX/TX frame queues are bounded (32 frames by default, configurable
up to 256). RX overflow or TX rechunker/queue overflow ends only that media
session and reports `ErrBackpressure`; the adapter listeners remain available
for a later call. Baresip's player uses a bounded kernel socket buffer with a
100 ms send deadline. An absent Go TX frame produces silence for that source
interval. Context cancellation closes listeners and connected sockets, which
unblocks the fixed accept/read/write workers and the Baresip source thread.

## Build against the installed Baresip ABI

The module is an external Baresip audio module. It was built against the public
headers from the upstream `v1.1.0` tag plus the notebook's installed `libre`
headers; it does not replace or update the Baresip executable or installed
modules. Build it with:

```sh
BARESIP_SOURCE=/path/to/baresip-v1.1.0 \
RE_INCLUDE_ROOT=/path/to/libre-headers \
OUT=/tmp/gru151_media.so \
./internal/telephony/baresipmedia/build-module.sh
```

The module loader resolves Baresip API symbols from the Baresip process. Load
the resulting `.so` from a test profile's `module_path`; do not modify the
working SIP account file or the ctrl_tcp listener to build or fixture-test
this media boundary.

GRU-152 local runtime startup creates the adapter first, then generates a
private Baresip profile from only the source `config` and `accounts` files.
It injects the adapter's actual `rx.sock`/`tx.sock` paths, loads
`gru151_media.so` and `ctrl_tcp.so`, and pins the control listener to
`127.0.0.1:4444`. The source profile is never edited. The generated profile
and copied credentials are mode-restricted and removed when the process exits.
The API supervises Baresip in the foreground and discards its SIP logs so
authorization headers cannot enter application logs. Startup fails unless
both OpenRouter JEV and Gemini Live credentials are configured; no call is
started by runtime initialization.

The v1.1.0 public header currently refers to `enum jbuf_type` before declaring
it. The external module supplies an ABI-sized enum declaration before including
`baresip.h`; this completes the public field type without changing the host
header or Baresip installation.

## C source parser / pacing gate (GRU-152 P0)

The TX source accepts exactly PCM S16LE/mono/24kHz with `ptime=20`: 960
payload bytes per 963-byte framed record. `stream_source.c` is compiled both
into the production `.so` and the C parser harness. It reads only the missing
header/payload bytes of one record per monotonic tick. Partial records survive
underrun (one silence frame); queued records remain in the bounded kernel
socket buffer and backpressure the bounded Go TX producer. A late callback
never causes catch-up playback faster than real time. Player/RTP codec and SIP
account behavior are unchanged.

No-provider tests:

```sh
sh internal/telephony/baresipmedia/module/test-parser.sh
BARESIP_SOURCE=/path/to/baresip-1.1.0 RE_INCLUDE_ROOT=/path/to/include C_TEST_OUTPUT_DIR=/tmp/media-gate sh internal/telephony/baresipmedia/test-module.sh
BARESIP_SOURCE=/path/to/baresip-1.1.0 RE_INCLUDE_ROOT=/path/to/include C_TEST_OUTPUT_DIR=/tmp/media-gate sh internal/telephony/baresipmedia/test-legacy-parser.sh
BARESIP_C_TEST_BIN=/tmp/media-gate/module-source-test BARESIP_C_TEST_MODULE=/tmp/media-gate/gru151_media.so go test -v -count=1 -run TestActualCModuleGeminiBurst ./internal/telephony/baresipmedia
```

`test-module.sh` loads the **actual production shared object**, invokes its
registered Baresip 1.1.0 ausrc allocator, and verifies PCM order, real cadence,
silence, and deterministic source destruction. Only the host registries are
stubbed; framing, librem/libre, socket IO, thread, callbacks, and destruction
execute production code. The Go integration test sends Gemini-equivalent 18
and 250 frame bursts through the real adapter to this source. It never starts
SIP or loads an account. CI runs this gate plus the reviewed old implementation
RED regression and the AddressSanitizer/UndefinedBehaviorSanitizer parser gate.

`gru151_media_stats` is a no-call ctrl_tcp/console command exposing fixed
process-lifetime counters (not PCM, paths, secrets or provider messages):
`source_frames_received`, `source_frames_emitted`, `source_silence_frames`,
`source_protocol_errors`, `source_socket_errors`, `source_max_buffered_bytes`,
`source_last_error_class`. The parser's user-space high water is at most 963
bytes, independently of burst length. Kernel queues and the Go queue are
bounded; no extra C queue is added.

The Call API retains first `gemini_audio_first_at`, `media_egress_first_at`,
`audio_error_at`, `call_failed_at`, and `call_closed_at` observations for each
call during the API process lifetime, and emits safe timestamped timeline logs.
`AUDIO_ERROR` is correlated by provider CallID when present, without changing
call state or dispatching Hangup. Only fixed error classes are captured, never
raw event parameters. These diagnostic slots are not persisted across API
restarts; transcript and canonical lifecycle persistence remain unchanged.
