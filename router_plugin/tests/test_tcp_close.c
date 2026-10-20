#define _GNU_SOURCE
#include "../src/tcp_proxy.c"
#include <assert.h>

/* Exercise the real poll/recv/send/shutdown lifecycle with socket pairs.
 * Include the implementation to arrange bounded pending data at EOF, without
 * adding diagnostic or test-only accessors to the production protocol. */
static unsigned remote_data,remote_close;
int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n)
{(void)e;(void)target;(void)path;(void)n;return -1;}
int vr_bind4(const char *ip,int port,int tcp)
{(void)ip;(void)port;(void)tcp;return -1;}
int vr_send(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,const void *data,size_t len)
{(void)e;(void)path;(void)n;(void)data;assert(main==8&&sub==3&&len>16);remote_data++;return 0;}
int vr_send_json(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,json_object *o)
{(void)e;(void)path;(void)n;(void)o;assert(main==8&&sub==4);remote_close++;return 0;}

typedef struct {vr_engine *e;tcp_session *s;int client,target;} fixture;
static fixture create(void)
{
    fixture f={0};int a[2],b[2];
    f.e=calloc(1,sizeof(*f.e));assert(f.e);strcpy(f.e->config.node_id,"7");
    f.s=allocate(f.e);assert(f.s);
    assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_NONBLOCK,0,a));
    assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_NONBLOCK,0,b));
    f.client=a[0];f.target=b[0];f.s->fd=a[1];f.s->up=b[1];f.s->phase=3;
    return f;
}
static int step(fixture *f)
{
    struct pollfd fds[129];size_t n=vr_tcp_pollfds(f->e,fds);
    int rc=poll(fds,n,0);assert(rc>=0);vr_tcp_poll(f->e,fds);return rc;
}
static void idle(fixture *f)
{
    /* Readiness is deterministic; do not use CPU/timing thresholds. */
    for(unsigned i=0;i<100;i++)assert(!step(f));
}
static void destroy(fixture *f)
{
    if(f->client>=0)close(f->client);
    if(f->target>=0)close(f->target);
    vr_tcp_stop(f->e);free(f->e);
}
static size_t fill_socket(int fd,uint8_t value)
{
    uint8_t bytes[4096];memset(bytes,value,sizeof(bytes));size_t total=0;ssize_t n;
    while((n=send(fd,bytes,sizeof(bytes),MSG_NOSIGNAL))>0)total+=(size_t)n;
    assert(n<0&&(errno==EAGAIN||errno==EWOULDBLOCK));return total;
}
static void fatal_write(int upstream)
{
    fixture f=create();size_t lane=upstream?0:1;
    memset(f.s->buffer[lane],42,34);f.s->length[lane]=34;
    if(upstream){close(f.target);f.target=-1;}else{close(f.client);f.client=-1;}
    step(&f);assert(!f.s->used);idle(&f);destroy(&f);
}
static void eof_backpressure(void)
{
    fixture f=create();size_t queued=fill_socket(f.s->fd,0x5a);
    memcpy(f.s->buffer[1],"final-bytes",11);f.s->length[1]=11;
    close(f.target);f.target=-1;step(&f);
    assert(f.s->used&&f.s->read_eof[1]&&f.s->length[1]==11);idle(&f);
    size_t got=0;int eof=0;uint8_t bytes[8192];
    for(unsigned i=0;i<1000&&!eof;i++) {
        step(&f);ssize_t n=recv(f.client,bytes,sizeof(bytes),0);
        if(n<0){assert(errno==EAGAIN||errno==EWOULDBLOCK);continue;}
        if(!n){eof=1;break;}
        for(ssize_t j=0;j<n;j++,got++){assert(got<queued+11);assert(bytes[j]==(got<queued?0x5a:(uint8_t)"final-bytes"[got-queued]));}
    }
    assert(eof&&got==queued+11);idle(&f);
    close(f.client);f.client=-1;step(&f);assert(!f.s->used);idle(&f);destroy(&f);
}
static void half_close(void)
{
    fixture f=create();int small=4096;assert(!setsockopt(f.s->up,SOL_SOCKET,SO_SNDBUF,&small,sizeof(small)));
    for(size_t i=0;i<TCP_BUFFER;i++)f.s->buffer[0][i]=(uint8_t)i;
    f.s->length[0]=TCP_BUFFER;assert(!shutdown(f.client,SHUT_WR));
    for(unsigned i=0;i<4;i++)step(&f);
    assert(f.s->read_eof[0]&&f.s->length[0]&&!f.s->write_shutdown[0]);idle(&f);
    uint8_t bytes[8192];size_t got=0;int eof=0;
    for(unsigned i=0;i<1000&&!eof;i++) {
        step(&f);ssize_t n=recv(f.target,bytes,sizeof(bytes),0);
        if(n<0){assert(errno==EAGAIN||errno==EWOULDBLOCK);continue;}
        if(!n){eof=1;break;}
        for(ssize_t j=0;j<n;j++,got++){assert(got<TCP_BUFFER);assert(bytes[j]==(uint8_t)got);}
    }
    assert(eof&&got==TCP_BUFFER&&f.s->used);idle(&f);
    assert(send(f.target,"response-after-fin",18,MSG_NOSIGNAL)==18);assert(!shutdown(f.target,SHUT_WR));
    got=0;eof=0;
    for(unsigned i=0;i<1000&&!eof;i++) {
        step(&f);ssize_t n=recv(f.client,bytes,sizeof(bytes),0);
        if(n<0){assert(errno==EAGAIN||errno==EWOULDBLOCK);continue;}
        if(!n){eof=1;break;}
        assert(got+(size_t)n<=18&&!memcmp(bytes,"response-after-fin"+got,(size_t)n));got+=(size_t)n;
    }
    assert(eof&&got==18&&!f.s->used);idle(&f);destroy(&f);
}
static void remote_drain(void)
{
    fixture f=create();close(f.s->up);f.s->up=-1;
    f.s->remote=1;f.s->count=2;strcpy(f.s->path[0],"7");strcpy(f.s->path[1],"8");
    memset(f.s->hex,'0',32);f.s->hex[32]=0;
    size_t queued=fill_socket(f.s->fd,0x5a);
    memcpy(f.s->buffer[1],"reply",5);f.s->length[1]=5;
    memcpy(f.s->buffer[0],"request",7);f.s->length[0]=7;
    const char *text="{\"session_id\":\"00000000000000000000000000000000\"}";
    char path[VR_PATH_MAX][129]={{"8"},{"7"}};vr_frame frame={8,4,NULL,(const uint8_t *)text,0,strlen(text)};
    vr_tcp_frame(f.e,&frame,path,2);assert(f.s->closing);step(&f);
    assert(f.s->used&&remote_data==1&&!remote_close);idle(&f);
    size_t got=0;uint8_t bytes[8192];
    for(unsigned i=0;i<1000&&got<queued+5;i++) {
        step(&f);ssize_t n=recv(f.client,bytes,sizeof(bytes),0);
        if(n<0){assert(errno==EAGAIN||errno==EWOULDBLOCK);continue;}
        assert(n>0);
        for(ssize_t j=0;j<n;j++,got++){assert(got<queued+5);assert(bytes[j]==(got<queued?0x5a:(uint8_t)"reply"[got-queued]));}
    }
    assert(got==queued+5&&!f.s->used&&remote_close==1);idle(&f);destroy(&f);
}
int main(void)
{
    fatal_write(0);fatal_write(1);eof_backpressure();half_close();remote_drain();
    puts("PASS: fatal writes reclaimed, EOF/backpressure idle, half-close response intact, remote close drains in order");return 0;
}
