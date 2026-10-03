#ifndef GRU151_STREAM_SOURCE_H
#define GRU151_STREAM_SOURCE_H
#include <stdatomic.h>
#include <stddef.h>
#include <stdint.h>
#include <time.h>

enum { GRU151_HEADER_BYTES = 3, GRU151_PCM_BYTES = 960, GRU151_PTIME_MS = 20 };
struct gru151_parser {
	uint8_t header[GRU151_HEADER_BYTES];
	uint8_t payload[GRU151_PCM_BYTES];
	size_t header_used, payload_used;
};
enum gru151_error_class { GRU151_NO_ERROR, GRU151_PROTOCOL, GRU151_PEER_CLOSED,
	GRU151_SOCKET, GRU151_CLOCK, GRU151_ALLOCATION };
struct gru151_source_stats {
	atomic_uint_fast64_t frames_received, frames_emitted, silence_frames;
	atomic_uint_fast64_t protocol_errors, socket_errors, max_buffered_bytes;
	atomic_int last_error_class;
};
/* Nonblocking; consumes AT MOST one record, preserving partial state.
 * 1 = PCM ready, 0 = incomplete/no data, negative errno = terminal error.
 * Remaining complete records stay in the bounded kernel socket buffer, which
 * applies producer backpressure. recv boundaries have no framing meaning. */
int gru151_read_frame(int fd, struct gru151_parser *p, void *pcm,
		      struct gru151_source_stats *stats);
/* Wait one ptime. Late callbacks reset the deadline rather than catching up
 * with an unpaced burst. EINTR retries the same absolute monotonic deadline. */
int gru151_source_wait(struct timespec *deadline);
const char *gru151_error_name(int error_class);
#endif
