/* Loads the production .so and calls its real ausrc allocator/read callback.
 * Only Baresip's registry is stubbed; libre/librem, framing, socket IO,
 * thread, pacing, and destruction are the actual module implementation.
 * No SIP account, provider, dial, RTP, or audio file is involved. */
#define _DEFAULT_SOURCE 1
#include <re.h>
#include <rem.h>
#include <baresip.h>
#include <assert.h>
#include <dlfcn.h>
#include <errno.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

void warning(const char *fmt, ...) { (void)fmt; }
static ausrc_alloc_h *source_alloc;
static struct list sources, players;
static const struct cmd *stats_cmd;
struct list *baresip_ausrcl(void) { return &sources; }
struct list *baresip_auplayl(void) { return &players; }
struct commands *baresip_commands(void) { return NULL; }
int cmd_register(struct commands *c, const struct cmd *v, size_t n)
{ (void)c; assert(n == 1); stats_cmd = v; return 0; }
void cmd_unregister(struct commands *c, const struct cmd *v)
{ (void)c; (void)v; stats_cmd = NULL; }
int ausrc_register(struct ausrc **p, struct list *l, const char *n, ausrc_alloc_h *h)
{ (void)l; (void)n; source_alloc = h; *p = mem_zalloc(sizeof(**p), NULL); return *p ? 0 : ENOMEM; }
int auplay_register(struct auplay **p, struct list *l, const char *n, auplay_alloc_h *h)
{ (void)l; (void)n; (void)h; *p = mem_zalloc(sizeof(**p), NULL); return *p ? 0 : ENOMEM; }

struct fixture {
	atomic_int frames, errors, corrupt, silence;
	int target;
	bool observe;
	bool inspect;
	struct timespec first, last;
	long min_gap_ns;
	int fd;
};
static long diff_ns(struct timespec a, struct timespec b)
{ return (a.tv_sec-b.tv_sec)*1000000000L + a.tv_nsec-b.tv_nsec; }
static void read_audio(struct auframe *af, void *arg)
{
	struct fixture *f = arg;
	const int16_t *p = af->sampv;
	struct timespec now;
	assert(af->fmt == AUFMT_S16LE && af->sampc == 480);
	clock_gettime(CLOCK_MONOTONIC, &now);
	if (f->observe) {
		bool real=false; for (size_t i=0;i<480;i++) if (p[i]) {real=true;break;}
		if (!real) {atomic_fetch_add(&f->silence,1);return;}
		if(f->inspect) { struct timespec real;clock_gettime(CLOCK_REALTIME,&real);printf("callback_sample=%d callback_unix_ns=%lld\n",p[0],(long long)real.tv_sec*1000000000LL+real.tv_nsec);fflush(stdout); }
		if (!atomic_load(&f->frames)) f->first=now;
		f->last=now;atomic_fetch_add(&f->frames,1);return;
	}
	if (p[0] == 0) {
		for (size_t i=0; i<480; i++) if (p[i]) atomic_fetch_add(&f->corrupt, 1);
		/* Pause the first callback so a complete burst is already buffered
		 * in the kernel before the next read: deterministic old-parser RED. */
		if (atomic_fetch_add(&f->silence, 1) == 0)
			usleep(100000);
		return;
	}
	int n = atomic_load(&f->frames);
	if (p[0] != n + 1) atomic_fetch_add(&f->corrupt, 1);
	for (size_t i = 1; i < 480; i++)
		if (p[i] != p[0]) { atomic_fetch_add(&f->corrupt, 1); break; }
	if (!n) f->first = now;
	else {
		long gap = diff_ns(now, f->last);
		if (gap < f->min_gap_ns) f->min_gap_ns = gap;
	}
	f->last = now;
	atomic_fetch_add(&f->frames, 1);
}
static void audio_error(int err, const char *str, void *arg)
{
	struct fixture *f = arg;
	(void)str; /* Never expose an arbitrary callback message. */
	atomic_fetch_add(&f->errors, 1);
	printf("actual_module_error_errno=%d\n", err);
}
static void send_exact(int fd, const uint8_t *p, size_t n)
{
	while (n) {
		ssize_t z = send(fd, p, n, MSG_NOSIGNAL);
		if (z < 0 && errno == EINTR) continue;
		assert(z > 0); p += z; n -= (size_t)z;
	}
}
static void *producer(void *arg)
{
	struct fixture *f = arg;
	while (!atomic_load(&f->silence)) usleep(1000);
	size_t bytes = (size_t)f->target * 963;
	uint8_t *burst = calloc(1, bytes);
	assert(burst);
	for (int i = 0; i < f->target; i++) {
		uint8_t *p = burst + i*963;
		p[0] = 0x13; p[1] = 3; p[2] = 192;
		for (int j = 0; j < 480; j++) {
			p[3+2*j] = (uint8_t)(i+1); p[4+2*j] = (uint8_t)((i+1)>>8);
		}
	}
	send_exact(f->fd, burst, bytes);
	free(burst);
	return NULL;
}
static int print_stats(const char *p, size_t n, void *arg)
{ (void)arg; return fwrite(p, 1, n, stdout) == n ? 0 : EIO; }
int main(int argc, char **argv)
{
	assert(argc == 3 || argc == 5 || argc == 6);
	bool external = argc >= 5;
	if (external) assert(!strcmp(argv[3], "--external"));
	void *module = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
	if (!module) { fprintf(stderr, "%s\n", dlerror()); return 1; }
	const struct mod_export *exports = dlsym(module, "exports");
	assert(exports && !exports->init() && source_alloc);
	char dir[] = "/tmp/gru151-source-XXXXXX";
	if (!external) assert(mkdtemp(dir));
	char path[108]; snprintf(path, sizeof(path), "%s/tx.sock", dir);
	int listener = -1;
	if (!external) listener = socket(AF_UNIX, SOCK_STREAM, 0);
	struct sockaddr_un addr = {.sun_family = AF_UNIX};
	strcpy(addr.sun_path, path);
	if (!external) assert(!bind(listener, (struct sockaddr *)&addr, sizeof(addr)) && !listen(listener, 1));
	else { assert(strlen(argv[4]) < sizeof(path)); strcpy(path, argv[4]); }
	struct fixture f = {.target = atoi(argv[2]), .min_gap_ns = 1000000000L};
	struct ausrc_prm prm = {.srate = 24000, .ch = 1, .ptime = 20, .fmt = AUFMT_S16LE};
	f.inspect = argc>5 && !strcmp(argv[5],"--inspect");
	f.observe = f.inspect || (argc>5 && !strcmp(argv[5],"--observe"));
	struct ausrc_st *source = NULL;
	assert(!source_alloc(&source, NULL, NULL, &prm, path, read_audio, audio_error, &f));
	pthread_t writer;
	if (!external) {
		f.fd = accept(listener, NULL, NULL); assert(f.fd >= 0);
		assert(!pthread_create(&writer, NULL, producer, &f));
	}
	for (int i=0; i < (f.observe ? 30000 : f.target*30+1000); i++) {
		if (atomic_load(&f.frames) >= f.target || atomic_load(&f.errors)) break;
		usleep(1000);
	}
	/* Remain connected for 3 silent ticks: underrun must not tear down. */
	usleep(80000);
	mem_deref(source); /* shutdown + join must finish before fixture cleanup */
	if (!external) pthread_join(writer, NULL);
	int received = atomic_load(&f.frames), errors = atomic_load(&f.errors);
	long duration = received > 1 ? diff_ns(f.last, f.first)/1000000 : 0;
	printf("actual_module_frames=%d expected=%d errors=%d corrupt=%d duration_ms=%ld min_gap_ms=%ld silence=%d\n", received, f.target, errors, atomic_load(&f.corrupt), duration, f.min_gap_ns/1000000, atomic_load(&f.silence));
	int ok = (f.observe ? received >= f.target : received == f.target) && !errors && !atomic_load(&f.corrupt) &&
		(f.observe || f.target == 1 || (duration >= (f.target-1)*18 && f.min_gap_ns >= 15000000));
	if (stats_cmd) { struct re_printf pf = {.vph = print_stats}; assert(!stats_cmd->h(&pf, NULL)); }
	if (!external) { close(f.fd); close(listener); unlink(path); rmdir(dir); }
	exports->close(); dlclose(module);
	return ok ? 0 : 1;
}
