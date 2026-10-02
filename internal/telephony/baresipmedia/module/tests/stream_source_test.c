#define _POSIX_C_SOURCE 200809L
#include "../stream_source.h"
#include <assert.h>
#include <errno.h>
#include <stdbool.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

/* Inject EINTR before the actual syscall, exercising the production retry. */
static bool inject_eintr;
ssize_t __real_recv(int, void *, size_t, int);
ssize_t __wrap_recv(int fd, void *p, size_t n, int flags)
{
	if (inject_eintr) { inject_eintr=false; errno=EINTR; return -1; }
	return __real_recv(fd,p,n,flags);
}
static void frame(uint8_t *p, int seq)
{
	p[0]=0x13; p[1]=3; p[2]=192;
	for (int i=0;i<480;i++) { p[3+i*2]=(uint8_t)seq; p[4+i*2]=(uint8_t)(seq>>8); }
}
static void put(int fd, const void *p, size_t n)
{ assert(send(fd,p,n,0)==(ssize_t)n); }
static void verify(const uint8_t *p, int seq)
{ for(int i=0;i<480;i++) assert(p[i*2]==(uint8_t)seq && p[i*2+1]==(uint8_t)(seq>>8)); }
static void fragmented(size_t header_part)
{
	int fd[2]; assert(!socketpair(AF_UNIX,SOCK_STREAM,0,fd));
	struct gru151_parser parser={0}; struct gru151_source_stats stats={0};
	uint8_t wire[963], pcm[960]; frame(wire,1);
	assert(gru151_read_frame(fd[1],&parser,pcm,&stats)==0); /* EAGAIN */
	put(fd[0],wire,header_part);
	assert(gru151_read_frame(fd[1],&parser,pcm,&stats)==0);
	for (size_t i=header_part;i<3;i++) {
		put(fd[0],wire+i,1);
		assert(gru151_read_frame(fd[1],&parser,pcm,&stats)==0);
	}
	put(fd[0],wire+3,117);
	assert(gru151_read_frame(fd[1],&parser,pcm,&stats)==0);
	assert(parser.payload_used==117);
	put(fd[0],wire+120,843);
	inject_eintr=true;
	assert(gru151_read_frame(fd[1],&parser,pcm,&stats)==1); verify(pcm,1);
	assert(atomic_load(&stats.max_buffered_bytes)==963);
	assert(!atomic_load(&stats.protocol_errors) && !atomic_load(&stats.socket_errors));
	close(fd[0]); close(fd[1]);
}
static void burst(int n)
{
	int fd[2]; assert(!socketpair(AF_UNIX,SOCK_STREAM,0,fd));
	struct gru151_parser parser={0}; struct gru151_source_stats stats={0};
	uint8_t wire[963*18+2], pcm[960];
	for(int i=0;i<n;i++) frame(wire+i*963,i+1);
	/* Includes a partial next header after all the valid records. */
	wire[n*963]=0x13;wire[n*963+1]=3;
	put(fd[0],wire,(size_t)n*963+2);
	for(int i=0;i<n;i++) { assert(gru151_read_frame(fd[1],&parser,pcm,&stats)==1);verify(pcm,i+1); }
	assert(gru151_read_frame(fd[1],&parser,pcm,&stats)==0 && parser.header_used==2);
	assert(atomic_load(&stats.frames_received)==(unsigned)n);
	assert(atomic_load(&stats.max_buffered_bytes)==963);
	assert(!atomic_load(&stats.protocol_errors) && !atomic_load(&stats.socket_errors));
	close(fd[0]);close(fd[1]);
}
static void invalid(uint8_t type, uint16_t length)
{
	int fd[2];assert(!socketpair(AF_UNIX,SOCK_STREAM,0,fd));
	struct gru151_parser p={0};struct gru151_source_stats s={0};uint8_t pcm[960];
	uint8_t h[]={type,(uint8_t)(length>>8),(uint8_t)length};put(fd[0],h,3);
	assert(gru151_read_frame(fd[1],&p,pcm,&s)==-EPROTO);
	assert(atomic_load(&s.protocol_errors)==1 && atomic_load(&s.last_error_class)==GRU151_PROTOCOL);
	close(fd[0]);close(fd[1]);
}
static void peer_close(void)
{
	int fd[2];assert(!socketpair(AF_UNIX,SOCK_STREAM,0,fd));
	struct gru151_parser p={0};struct gru151_source_stats s={0};uint8_t pcm[960],wire[963];frame(wire,1);
	put(fd[0],wire,963);close(fd[0]);
	/* A readable frame must be drained even when peer has already closed. */
	assert(gru151_read_frame(fd[1],&p,pcm,&s)==1);verify(pcm,1);
	assert(gru151_read_frame(fd[1],&p,pcm,&s)==-ECONNRESET);
	assert(atomic_load(&s.last_error_class)==GRU151_PEER_CLOSED);close(fd[1]);
}
int main(void)
{
	fragmented(1);fragmented(2);burst(1);burst(2);burst(18);
	invalid(0x12,960);invalid(0x13,959);invalid(0x13,0);invalid(0x13,65535);peer_close();
	puts("C parser PASS: single/fragmented header 1+1+1,2+1/payload/coalesced 2,18/partial next/EINTR/EAGAIN/invalid/EOF; max_buffered=963");
	return 0;
}
