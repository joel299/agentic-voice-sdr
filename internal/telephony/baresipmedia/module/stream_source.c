#define _POSIX_C_SOURCE 200809L
#include "stream_source.h"
#include <errno.h>
#include <stdbool.h>
#include <string.h>
#include <sys/socket.h>

static void buffered(struct gru151_parser *p, struct gru151_source_stats *s)
{
	uint_fast64_t n = p->header_used + p->payload_used;
	uint_fast64_t high = atomic_load(&s->max_buffered_bytes);
	while (high < n && !atomic_compare_exchange_weak(&s->max_buffered_bytes, &high, n)) {}
}
int gru151_read_frame(int fd, struct gru151_parser *p, void *pcm,
		      struct gru151_source_stats *s)
{
	for (;;) {
		bool header = p->header_used < GRU151_HEADER_BYTES;
		uint8_t *dest = header ? p->header + p->header_used : p->payload + p->payload_used;
		size_t remaining = header ? GRU151_HEADER_BYTES-p->header_used : GRU151_PCM_BYTES-p->payload_used;
		ssize_t n = recv(fd, dest, remaining, MSG_DONTWAIT);
		if (n < 0) {
			if (errno == EINTR) continue;
			if (errno == EAGAIN || errno == EWOULDBLOCK) return 0;
			atomic_fetch_add(&s->socket_errors, 1);
			atomic_store(&s->last_error_class, GRU151_SOCKET);
			return -errno;
		}
		if (!n) {
			atomic_fetch_add(&s->socket_errors, 1);
			atomic_store(&s->last_error_class, GRU151_PEER_CLOSED);
			return -ECONNRESET;
		}
		if (header) p->header_used += (size_t)n;
		else p->payload_used += (size_t)n;
		buffered(p, s);
		if (p->header_used == GRU151_HEADER_BYTES &&
		    (p->header[0] != 0x13 || ((unsigned)p->header[1]<<8 | p->header[2]) != GRU151_PCM_BYTES)) {
			atomic_fetch_add(&s->protocol_errors, 1);
			atomic_store(&s->last_error_class, GRU151_PROTOCOL);
			return -EPROTO;
		}
		if (p->payload_used == GRU151_PCM_BYTES) {
			memcpy(pcm, p->payload, GRU151_PCM_BYTES);
			p->header_used = p->payload_used = 0;
			atomic_fetch_add(&s->frames_received, 1);
			return 1;
		}
	}
}
int gru151_source_wait(struct timespec *deadline)
{
	struct timespec now;
	int err;
	if (clock_gettime(CLOCK_MONOTONIC, &now)) return errno;
	deadline->tv_nsec += GRU151_PTIME_MS * 1000000L;
	if (deadline->tv_nsec >= 1000000000L) {
		deadline->tv_sec++;
		deadline->tv_nsec -= 1000000000L;
	}
	/* Keep the absolute cadence under normal callback load. If an entire
	 * tick was missed, schedule a fresh tick instead of catching up. */
	if (deadline->tv_sec < now.tv_sec ||
	    (deadline->tv_sec == now.tv_sec && deadline->tv_nsec <= now.tv_nsec)) {
		*deadline = now;
		deadline->tv_nsec += GRU151_PTIME_MS * 1000000L;
		if (deadline->tv_nsec >= 1000000000L) {
			deadline->tv_sec++;
			deadline->tv_nsec -= 1000000000L;
		}
	}
	do { err = clock_nanosleep(CLOCK_MONOTONIC, TIMER_ABSTIME, deadline, NULL); } while (err == EINTR);
	return err;
}
const char *gru151_error_name(int c)
{
	switch (c) {
	case GRU151_NO_ERROR: return "none";
	case GRU151_PROTOCOL: return "frame_protocol";
	case GRU151_PEER_CLOSED: return "peer_closed";
	case GRU151_SOCKET: return "socket_io";
	case GRU151_CLOCK: return "clock";
	case GRU151_ALLOCATION: return "allocation";
	default: return "unknown";
	}
}
