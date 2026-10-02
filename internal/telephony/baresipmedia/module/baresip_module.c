#define _DEFAULT_SOURCE 1
#include <re.h>
#include <rem.h>
/* Baresip 1.1.0's installed public header uses this enum in struct config
 * without including its declaration. The module does not inspect the field;
 * completing the enum here preserves the public struct's integer ABI layout. */
#ifdef GRU151_DECLARE_JBUF_TYPE
enum jbuf_type { JBUF_OFF = 0, JBUF_FIXED, JBUF_ADAPTIVE };
#endif
#include <baresip.h>
#include "stream_source.h"
#include <errno.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

enum { RX_TYPE_SLIN16 = 0x12, TX_TYPE_SLIN24 = 0x13, FRAME_HEADER = 3,
	MAX_FRAME_BYTES = 16384, DEFAULT_PTIME_MS = 20 };

struct media_io {
	int fd;
	bool source;
	atomic_bool running;
	atomic_bool failed;
	pthread_t thread;
	bool thread_started;
	uint32_t srate;
	uint8_t channels;
	uint32_t ptime;
	size_t sample_count;
	size_t payload_bytes;

	ausrc_read_h *readh;
	ausrc_error_h *errorh;
	auplay_write_h *writeh;
	void *arg;
};

struct ausrc_st { struct media_io *io; };
struct auplay_st { struct media_io *io; };

static struct ausrc *ausrc;
static struct auplay *auplay;

static void source_destructor(void *arg);
static void *player_thread(void *arg);

static int connect_unix(const char *path)
{
	struct sockaddr_un addr;
	struct timeval snd_timeout = {.tv_sec = 0, .tv_usec = 100000};
	int fd;
	int sndbuf = 4096;

	if (!path || !*path || strlen(path) >= sizeof(addr.sun_path))
		return -EINVAL;
	fd = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
	if (fd < 0)
		return -errno;
	memset(&addr, 0, sizeof(addr));
	addr.sun_family = AF_UNIX;
	strncpy(addr.sun_path, path, sizeof(addr.sun_path) - 1);
	if (connect(fd, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
		int err = errno;
		close(fd);
		return -err;
	}
	(void)setsockopt(fd, SOL_SOCKET, SO_SNDBUF, &sndbuf, sizeof(sndbuf));
	(void)setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &snd_timeout, sizeof(snd_timeout));
	return fd;
}

static int send_all(int fd, const uint8_t *buf, size_t len)
{
	while (len) {
		ssize_t n = send(fd, buf, len, MSG_NOSIGNAL);
		if (n < 0) {
			if (errno == EINTR)
				continue;
			return -errno;
		}
		if (!n)
			return -EPIPE;
		buf += n;
		len -= (size_t)n;
	}
	return 0;
}

static void media_stop(struct media_io *io)
{
	if (!io)
		return;
	atomic_store(&io->running, false);
	if (io->fd >= 0)
		(void)shutdown(io->fd, SHUT_RDWR);
}

static void media_destructor(void *arg)
{
	struct media_io *io = arg;
	media_stop(io);
	if (io->thread_started)
		(void)pthread_join(io->thread, NULL);
	if (io->fd >= 0)
		close(io->fd);
}

static int validate_params(uint32_t rate, uint8_t channels, uint32_t ptime,
			   int fmt, bool source)
{
	uint32_t expected_rate = source ? 24000 : 16000;
	if (rate != expected_rate || channels != 1 || fmt != AUFMT_S16LE)
		return EINVAL;
	if ((source && ptime != GRU151_PTIME_MS) || ptime == 0 || ptime > 60 || (rate * ptime) % 1000)
		return EINVAL;
	return 0;
}

static struct gru151_source_stats source_stats;

static void *source_thread(void *arg)
{
	struct media_io *io = arg;
	struct gru151_parser parser = {0};
	int16_t samples[GRU151_PCM_BYTES / sizeof(int16_t)];
	struct timespec deadline;
	if (clock_gettime(CLOCK_MONOTONIC, &deadline)) {
		atomic_store(&source_stats.last_error_class, GRU151_CLOCK);
		atomic_store(&io->failed, true);
		if (io->errorh) io->errorh(errno, "gru151_clock", io->arg);
		return NULL;
	}
	while (atomic_load(&io->running)) {
		struct auframe af;
		int err = gru151_source_wait(&deadline);
		if (!atomic_load(&io->running)) break;
		if (err) {
			atomic_store(&source_stats.last_error_class, GRU151_CLOCK);
			atomic_store(&io->failed, true);
			if (io->errorh) io->errorh(err, "gru151_clock", io->arg);
			break;
		}
		err = gru151_read_frame(io->fd, &parser, samples, &source_stats);
		if (err < 0) {
			atomic_store(&io->failed, true);
			/* The error callback may close the call and release this source.
			 * Do not access io after invoking it. */
			if (atomic_load(&io->running) && io->errorh)
				io->errorh(-err, err == -EPROTO ? "gru151_frame_protocol" : "gru151_socket_io", io->arg);
			break;
		}
		if (!err) {
			memset(samples, 0, sizeof(samples));
			atomic_fetch_add(&source_stats.silence_frames, 1);
		}
		auframe_init(&af, AUFMT_S16LE, samples, io->sample_count);
		af.timestamp = tmr_jiffies() * 1000;
		if (io->readh) io->readh(&af, io->arg);
		atomic_fetch_add(&source_stats.frames_emitted, 1);
	}
	return NULL;
}

static int source_alloc(struct ausrc_st **stp, const struct ausrc *as,
			struct media_ctx **ctx, struct ausrc_prm *prm,
			const char *device, ausrc_read_h *readh,
			ausrc_error_h *errorh, void *arg)
{
	struct ausrc_st *st;
	struct media_io *io;
	int err;
	(void)as;
	(void)ctx;
	if (!stp || !prm || !readh)
		return EINVAL;
	if ((err = validate_params(prm->srate, prm->ch, prm->ptime,
				   prm->fmt, true)))
		return err;
	st = mem_zalloc(sizeof(*st), source_destructor);
	io = mem_zalloc(sizeof(*io), media_destructor);
	if (io)
		io->fd = -1;
	if (!st || !io) {
		mem_deref(st);
		mem_deref(io);
		return ENOMEM;
	}
	io->fd = connect_unix(device);
	if (io->fd < 0) {
		err = -io->fd;
		io->fd = -1;
		mem_deref(io);
		mem_deref(st);
		return err;
	}
	io->source = true;
	io->srate = prm->srate;
	io->channels = prm->ch;
	io->ptime = prm->ptime ? prm->ptime : DEFAULT_PTIME_MS;
	io->sample_count = (size_t)io->srate * io->channels * io->ptime / 1000;
	io->payload_bytes = io->sample_count * sizeof(int16_t);
	io->readh = readh;
	io->errorh = errorh;
	io->arg = arg;
	if (io->payload_bytes > MAX_FRAME_BYTES) {
		mem_deref(io);
		mem_deref(st);
		return EMSGSIZE;
	}
	atomic_store(&io->running, true);
	err = pthread_create(&io->thread, NULL, source_thread, io);
	if (err) {
		mem_deref(io);
		mem_deref(st);
		return err;
	}
	io->thread_started = true;
	st->io = io;
	*stp = st;
	return 0;
}

static void source_destructor(void *arg)
{
	struct ausrc_st *st = arg;
	mem_deref(st->io);
}

static void player_destructor(void *arg)
{
	struct auplay_st *st = arg;
	mem_deref(st->io);
}

static int player_alloc(struct auplay_st **stp, const struct auplay *ap,
			struct auplay_prm *prm, const char *device,
			auplay_write_h *writeh, void *arg)
{
	struct auplay_st *st;
	struct media_io *io;
	int err;
	(void)ap;
	if (!stp || !prm || !writeh)
		return EINVAL;
	if ((err = validate_params(prm->srate, prm->ch, prm->ptime,
				   prm->fmt, false)))
		return err;
	st = mem_zalloc(sizeof(*st), player_destructor);
	io = mem_zalloc(sizeof(*io), media_destructor);
	if (io)
		io->fd = -1;
	if (!st || !io) {
		mem_deref(st);
		mem_deref(io);
		return ENOMEM;
	}
	io->fd = connect_unix(device);
	if (io->fd < 0) {
		err = -io->fd;
		io->fd = -1;
		mem_deref(io);
		mem_deref(st);
		return err;
	}
	io->srate = prm->srate;
	io->channels = prm->ch;
	io->ptime = prm->ptime ? prm->ptime : DEFAULT_PTIME_MS;
	io->sample_count = (size_t)io->srate * io->channels * io->ptime / 1000;
	io->payload_bytes = io->sample_count * sizeof(int16_t);
	io->writeh = writeh;
	io->arg = arg;
	if (io->payload_bytes > MAX_FRAME_BYTES) {
		mem_deref(io);
		mem_deref(st);
		return EMSGSIZE;
	}
	atomic_store(&io->running, true);
	err = pthread_create(&io->thread, NULL, player_thread, io);
	if (err) {
		mem_deref(io);
		mem_deref(st);
		return err;
	}
	io->thread_started = true;
	st->io = io;
	*stp = st;
	return 0;
}

static int player_send_frame(struct media_io *io, const struct auframe *af)
{
	uint8_t hdr[FRAME_HEADER];
	size_t bytes;
	int err;
	if (!af || atomic_load(&io->failed))
		return -ECANCELED;
	bytes = af->sampc * sizeof(int16_t);
	if (af->fmt != AUFMT_S16LE || af->sampc != io->sample_count ||
	    !af->sampv || bytes == 0 || bytes > MAX_FRAME_BYTES || bytes > 0xffff)
		return -EINVAL;
	hdr[0] = RX_TYPE_SLIN16;
	hdr[1] = (uint8_t)(bytes >> 8);
	hdr[2] = (uint8_t)bytes;
	err = send_all(io->fd, hdr, sizeof(hdr));
	if (!err)
		err = send_all(io->fd, af->sampv, bytes);
	return err;
}

static void *player_thread(void *arg)
{
	struct media_io *io = arg;
	int16_t *samples = calloc(io->sample_count, sizeof(*samples));
	if (!samples) {
		atomic_store(&io->failed, true);
		media_stop(io);
		return NULL;
	}
	while (atomic_load(&io->running)) {
		struct auframe af;
		int err;
		struct timespec deadline;
		auframe_init(&af, AUFMT_S16LE, samples, io->sample_count);
		af.timestamp = tmr_jiffies() * 1000;
		io->writeh(&af, io->arg);
		err = player_send_frame(io, &af);
		if (err) {
			atomic_store(&io->failed, true);
			media_stop(io);
			break;
		}
		clock_gettime(CLOCK_MONOTONIC, &deadline);
		deadline.tv_nsec += (long)io->ptime * 1000000L;
		if (deadline.tv_nsec >= 1000000000L) {
			deadline.tv_sec++;
			deadline.tv_nsec -= 1000000000L;
		}
		while (atomic_load(&io->running) &&
		       clock_nanosleep(CLOCK_MONOTONIC, TIMER_ABSTIME, &deadline, NULL) == EINTR) {}
	}
	free(samples);
	return NULL;
}

/* Process-lifetime counters, fixed class vocabulary, no payload/paths/secrets. */
static int source_stats_print(struct re_printf *pf, void *arg)
{
	(void)arg;
	return re_hprintf(pf,
		"source_frames_received=%llu source_frames_emitted=%llu source_silence_frames=%llu "
		"source_protocol_errors=%llu source_socket_errors=%llu source_max_buffered_bytes=%llu source_last_error_class=%s\n",
		(unsigned long long)atomic_load(&source_stats.frames_received),
		(unsigned long long)atomic_load(&source_stats.frames_emitted),
		(unsigned long long)atomic_load(&source_stats.silence_frames),
		(unsigned long long)atomic_load(&source_stats.protocol_errors),
		(unsigned long long)atomic_load(&source_stats.socket_errors),
		(unsigned long long)atomic_load(&source_stats.max_buffered_bytes),
		gru151_error_name(atomic_load(&source_stats.last_error_class)));
}
static const struct cmd cmdv[] = {
	{"gru151_media_stats", 0, 0, "Safe media source counters", source_stats_print},
};

static int module_init(void)
{
	int err = ausrc_register(&ausrc, baresip_ausrcl(), "gru151_media", source_alloc);
	if (err)
		return err;
	err = auplay_register(&auplay, baresip_auplayl(), "gru151_media", player_alloc);
	if (err) {
		ausrc = mem_deref(ausrc);
		return err;
	}
	err = cmd_register(baresip_commands(), cmdv, 1);
	if (err) { ausrc = mem_deref(ausrc); auplay = mem_deref(auplay); }
	return err;
}

static int module_close(void)
{
	cmd_unregister(baresip_commands(), cmdv);
	auplay = mem_deref(auplay);
	ausrc = mem_deref(ausrc);
	return 0;
}

EXPORT_SYM const struct mod_export DECL_EXPORTS(gru151_media) = {
	"gru151_media",
	"audio",
	module_init,
	module_close,
};
