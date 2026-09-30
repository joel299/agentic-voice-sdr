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
