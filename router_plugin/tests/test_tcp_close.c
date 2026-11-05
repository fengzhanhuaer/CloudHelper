#define _GNU_SOURCE
#include "../src/tcp_proxy.c"
#include <assert.h>

/* Exercise the real poll/recv/send/shutdown lifecycle with socket pairs.
 * Include the implementation to arrange bounded pending data at EOF, without
 * adding diagnostic or test-only accessors to the production protocol. */
static unsigned remote_data,remote_close,cache_epoch,cache_adds,cache_deletes,cache_clears,path_calls;
static int cache_fail;
static uint64_t test_now;
uint64_t __real_vr_millis(void);
uint64_t __wrap_vr_millis(void){return test_now?test_now:__real_vr_millis();}
unsigned vr_platform_tls_cache_epoch(void){return cache_epoch;}
int vr_platform_tls_cache_clear(void){if(cache_fail)return -1;cache_epoch++;cache_clears++;return 0;}
int vr_platform_tls_cache(const char *ip,int add)
{assert(*ip);if(cache_fail)return -1;if(add)cache_adds++;else cache_deletes++;return 0;}
int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n)
{(void)e;(void)path;(void)n;assert(!strcmp(target,"8"));path_calls++;return -1;}
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
    assert(!f.s->buffer[0]&&!f.s->buffer[1]);
    assert(!reserve(f.s,0,1)&&f.s->capacity[0]==4096);
    assert(!reserve(f.s,0,4097)&&f.s->capacity[0]==8192);
    assert(!reserve(f.s,0,8193)&&f.s->capacity[0]==12288);
    assert(!reserve(f.s,0,TCP_BUFFER)&&!reserve(f.s,1,TCP_BUFFER));
    assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_NONBLOCK,0,a));
    assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_NONBLOCK,0,b));
    f.client=a[0];f.target=b[0];f.s->fd=a[1];f.s->up=b[1];f.s->phase=3;
    return f;
}
static int step(fixture *f)
{
    struct pollfd fds[VR_TCP_POLL_MAX];size_t n=vr_tcp_pollfds(f->e,fds);
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
    step(&f);assert(!f.e->tcp->sessions[0]);idle(&f);destroy(&f);
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
    close(f.client);f.client=-1;step(&f);assert(!f.e->tcp->sessions[0]);idle(&f);destroy(&f);
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
    assert(eof&&got==18&&!f.e->tcp->sessions[0]);idle(&f);destroy(&f);
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
    assert(got==queued+5&&!f.e->tcp->sessions[0]&&remote_close==1);idle(&f);destroy(&f);
}
static void idle_timeout(void)
{
    test_now=1000000;fixture f=create();
    test_now+=600000;step(&f);assert(f.s->used);
    test_now++;step(&f);assert(!f.e->tcp->sessions[0]);
    destroy(&f);test_now=0;
}
static void sni_cache(void)
{
    test_now=1000000;fixture f=create();vr_service service={0};
    pthread_mutex_init(&service.mutex,NULL);f.e->service=&service;f.e->config.tls_sni_enabled=1;
    tcp_session temp={0};temp.port=443;strcpy(temp.ip,"203.0.113.5");strcpy(temp.domain,"fixture.example");
    vr_decision direct={.action=VR_DIRECT},reject={.action=VR_REJECT},proxy={.action=VR_PROXY};strcpy(proxy.exit_node,"8");
    unsigned adds=cache_adds,deletes=cache_deletes,clears=cache_clears;
    cache_store(f.e,&temp,direct);assert(cache_adds==adds+1&&f.e->tcp->cache[0].direct);
    uint64_t expiry=f.e->tcp->cache[0].expires;assert(expiry-test_now==72ull*60*60*1000);
    const char *rules="{\"route_rules\":[{\"id\":\"one\",\"action\":\"probe_exit\",\"exit_node_id\":\"8\",\"entries\":[\"domain:fixture.example\"]}]}";
    f.e->policy=vr_json_parse(rules,strlen(rules));assert(f.e->policy);
    assert(f.e->tcp->cache[0].expires==expiry);
    test_now=expiry;cache_fail=1;cache_tick(f.e);assert(cache_deletes==deletes&&f.e->tcp->cache[0].expires);
    cache_fail=0;test_now+=5000;cache_tick(f.e);assert(cache_clears==clears+1&&cache_deletes==deletes&&!f.e->tcp->cache[0].expires);
    /* Neither proxy nor reject entries install kernel bypasses. */
    assert(sizeof(tcp_sni_cache)==16);strcpy(proxy.rule,"one");
    adds=cache_adds;cache_store(f.e,&temp,proxy);assert(!f.e->tcp->cache[0].direct&&cache_adds==adds&&f.e->tcp->cache[0].route==0);
    /* The cached rule ordinal chooses its exit before any ClientHello. A
       missing carrier must close, never fall back to the physical route. */
    int pair[2];assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_NONBLOCK,0,pair));unsigned paths=path_calls;
    assert(!vr_tcp_attach(f.e,pair[1],temp.ip,443));char byte;assert(!read(pair[0],&byte,1));close(pair[0]);assert(path_calls==paths+1);
    cache_store(f.e,&temp,reject);assert(!f.e->tcp->cache[0].direct&&cache_adds==adds);
    assert(f.e->tcp->cache[0].route==-2);
    assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_NONBLOCK,0,pair));assert(!vr_tcp_attach(f.e,pair[1],temp.ip,443));assert(!read(pair[0],&byte,1));close(pair[0]);assert(path_calls==paths+1);
    cache_epoch++;cache_tick(f.e);assert(!f.e->tcp->cache[0].expires);
    /* Bounded eviction removes the old direct rule before reusing its slot. */
    json_object_put(f.e->policy);f.e->policy=NULL;
    for(unsigned i=0;i<TCP_CACHE_MAX+1;i++) {
        snprintf(temp.ip,sizeof(temp.ip),"203.0.%u.%u",i/256,i%256);test_now++;cache_store(f.e,&temp,direct);
    }
    unsigned count=0;for(unsigned i=0;i<TCP_CACHE_MAX;i++){count+=!!f.e->tcp->cache[i].expires;assert(f.e->tcp->cache[i].ip!=inet_addr("203.0.0.0"));}
    assert(count==TCP_CACHE_MAX&&cache_deletes==deletes+1);
    clears=cache_clears;test_now=f.e->tcp->cache[1].expires;uint64_t retained=f.e->tcp->cache[2].expires;
    cache_tick(f.e);assert(cache_clears==clears+1&&!f.e->tcp->cache[1].expires);
    assert(f.e->tcp->cache[2].expires==retained&&!f.e->tcp->cache[2].direct);
    adds=cache_adds;cache_direct(f.e,&f.e->tcp->cache[2],"203.0.0.2");
    assert(cache_adds==adds+1&&f.e->tcp->cache[2].direct&&f.e->tcp->cache[2].expires==retained);
    cache_epoch++;cache_tick(f.e);for(unsigned i=0;i<TCP_CACHE_MAX;i++)assert(!f.e->tcp->cache[i].expires);
    temp.port=80;adds=cache_adds;cache_store(f.e,&temp,direct);assert(cache_adds==adds);
    printf("PASS: 600s idle, 72h IP route cache, %d entries x %zu bytes, immediate cached proxy/reject, eviction and invalidation\n",TCP_CACHE_MAX,sizeof(tcp_sni_cache));
    destroy(&f);pthread_mutex_destroy(&service.mutex);test_now=0;
}
int main(void)
{
    fatal_write(0);fatal_write(1);eof_backpressure();half_close();remote_drain();idle_timeout();sni_cache();
    puts("PASS: fatal writes reclaimed, EOF/backpressure idle, half-close response intact, remote close drains in order");return 0;
}
