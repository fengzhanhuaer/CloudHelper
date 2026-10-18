#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#define TCP_MAX 64
#define TCP_BUFFER 65536
#define ORIGINAL_DST 80
typedef struct {
    int used,fd,up,phase,remote,exit_role,closing;
    uint64_t born,active;
    uint8_t id[16];char hex[33],ip[16],domain[254];int port;
    char path[VR_PATH_MAX][129];size_t count;
    uint8_t buffer[2][TCP_BUFFER];size_t length[2];
} tcp_session;
typedef struct {int state,ok;char id[33],domain[254],ip[16];} tcp_dns_job;
struct vr_tcp_state {
    int listener,started,stop;pthread_t thread;pthread_mutex_t mutex;pthread_cond_t changed;
    tcp_dns_job jobs[8];tcp_session sessions[TCP_MAX];
};

static int public_ip(const char *s)
{
    struct in_addr a;if(inet_pton(AF_INET,s,&a)!=1)return 0;uint32_t v=ntohl(a.s_addr);
    return v>=0x01000000u&&v<0xe0000000u&&(v>>24)!=10&&(v>>24)!=127&&
        (v&0xffff0000u)!=0xa9fe0000u&&(v&0xfff00000u)!=0xac100000u&&
        (v&0xffff0000u)!=0xc0a80000u&&(v&0xffc00000u)!=0x64400000u&&
        (v&0xfffe0000u)!=0xc6120000u;
}
static int hex_id(const char *s,uint8_t id[16])
{
    if(strlen(s)!=32)return -1;
    for(size_t i=0;i<16;i++) {
        unsigned value=0;
        for(size_t j=0;j<2;j++) {unsigned char c=(unsigned char)s[i*2+j];
            if(c>='0'&&c<='9')value=value*16+c-'0';
            else if(c>='a'&&c<='f')value=value*16+c-'a'+10;else return -1;}
        id[i]=(uint8_t)value;
    }
    return 0;
}
static json_object *message(tcp_session *s)
{json_object *o=json_object_new_object();json_object_object_add(o,"session_id",json_object_new_string(s->hex));return o;}
static void drop(vr_engine *e,tcp_session *s,int notify)
{
    if(!s->used)return;
    if(notify&&s->remote&&s->count>=2) {json_object *o=message(s);vr_send_json(e,8,4,s->path,s->count,o);json_object_put(o);}
    if(s->fd>=0)close(s->fd);
    if(s->up>=0)close(s->up);
    memset(s,0,sizeof(*s));s->fd=s->up=-1;
}
static tcp_session *allocate(vr_engine *e)
{
    if(!e->tcp) {e->tcp=calloc(1,sizeof(*e->tcp));if(!e->tcp)return NULL;e->tcp->listener=-1;
        pthread_mutex_init(&e->tcp->mutex,NULL);pthread_cond_init(&e->tcp->changed,NULL);}
    for(size_t i=0;i<TCP_MAX;i++)if(!e->tcp->sessions[i].used) {
        tcp_session *s=&e->tcp->sessions[i];memset(s,0,sizeof(*s));s->used=1;s->fd=s->up=-1;s->born=s->active=vr_millis();return s;}
    return NULL;
}
static int connect_ip(tcp_session *s)
{
    struct sockaddr_in dst={0};dst.sin_family=AF_INET;dst.sin_port=htons((uint16_t)s->port);
    if(inet_pton(AF_INET,s->ip,&dst.sin_addr)!=1)return -1;
    s->up=socket(AF_INET,SOCK_STREAM|SOCK_NONBLOCK|SOCK_CLOEXEC,0);if(s->up<0)return -1;
    int rc=connect(s->up,(struct sockaddr *)&dst,sizeof(dst));if(rc&&errno!=EINPROGRESS)return -1;
    s->phase=2;return 0;
}
static void result(vr_engine *e,tcp_session *s,int ok)
{
    json_object *o=message(s);json_object_object_add(o,"ok",json_object_new_boolean(ok));
    if(!ok)json_object_object_add(o,"error",json_object_new_string("target unavailable or not authorized"));
    vr_send_json(e,8,2,s->path,s->count,o);json_object_put(o);
}
static void decide(vr_engine *e,tcp_session *s)
{
    vr_decision d=vr_policy_match(e->policy,s->domain,s->ip);
    if(*s->domain) {char event[240];snprintf(event,sizeof(event),"SNI %.150s: %s",s->domain,d.action==VR_REJECT?"reject":d.action==VR_PROXY?"probe_exit":"direct");vr_service_log(e->service,"tls",event);}
    if(d.action==VR_REJECT){drop(e,s,0);return;}
    if(d.action!=VR_PROXY||!strcmp(d.exit_node,e->config.node_id)) {if(connect_ip(s))drop(e,s,0);return;}
    if(!*d.exit_node||vr_path(e,d.exit_node,s->path,&s->count)||s->count<2||
        vr_random_hex(s->hex,16)||hex_id(s->hex,s->id)){drop(e,s,0);return;}
    s->remote=1;s->phase=1;char addr[280];snprintf(addr,sizeof(addr),"%s:%d",*s->domain?s->domain:s->ip,s->port);
    json_object *o=message(s);json_object_object_add(o,"target_addr",json_object_new_string(addr));
    json_object_object_add(o,"source_node_id",json_object_new_string(e->config.node_id));
    json_object_object_add(o,"exit_node_id",json_object_new_string(d.exit_node));
    if(vr_send_json(e,8,1,s->path,s->count,o))drop(e,s,0);
    json_object_put(o);
}

int vr_tcp_start(vr_engine *e)
{
    if(!e->tcp) {tcp_session *s=allocate(e);if(!s)return -1;drop(e,s,0);}
    if(e->tcp->listener>=0)return 0;
    e->tcp->listener=vr_bind4(e->addresses.lan,1054,1);
    return e->tcp->listener<0?-1:0;
}
static void *dns_worker(void *arg)
{
    vr_engine *e=arg;struct vr_tcp_state *t=e->tcp;
    for(;;) {
        pthread_mutex_lock(&t->mutex);size_t i;
        for(;;) {for(i=0;i<8;i++)if(t->jobs[i].state==1)break;
            if(t->stop||i<8)break;
            pthread_cond_wait(&t->changed,&t->mutex);}
        if(t->stop){pthread_mutex_unlock(&t->mutex);return NULL;}
        tcp_dns_job job=t->jobs[i];t->jobs[i].state=2;pthread_mutex_unlock(&t->mutex);
        job.ok=!vr_resolve_config4(job.domain,&e->config,e->addresses.wan_if,job.ip);
        pthread_mutex_lock(&t->mutex);t->jobs[i]=job;t->jobs[i].state=3;pthread_mutex_unlock(&t->mutex);
    }
}
static int resolve_async(vr_engine *e,tcp_session *s)
{
    struct vr_tcp_state *t=e->tcp;
    if(!t->started) {
        pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,256*1024);
        int rc=pthread_create(&t->thread,&attr,dns_worker,e);pthread_attr_destroy(&attr);if(rc)return -1;t->started=1;
    }
    int rc=-1;pthread_mutex_lock(&t->mutex);
    for(size_t i=0;i<8;i++)if(!t->jobs[i].state) {tcp_dns_job *j=&t->jobs[i];memset(j,0,sizeof(*j));
        strcpy(j->domain,s->domain);strcpy(j->id,s->hex);j->state=1;s->phase=4;pthread_cond_signal(&t->changed);rc=0;break;}
    pthread_mutex_unlock(&t->mutex);return rc;
}
static int authorized(vr_engine *e,tcp_session *s)
{
    if(!public_ip(s->ip))return vr_gateway_allowed(&e->config.gateway,s->path[s->count-1])&&vr_gateway_contains(&e->config.gateway,s->ip);
    vr_decision d=vr_policy_match(e->policy,s->domain,s->ip);
    return d.action==VR_PROXY&&(!strcmp(d.exit_node,e->config.node_id)||!strcmp(d.backup,e->config.node_id));
}
static void dns_results(vr_engine *e)
{
    for(size_t i=0;i<8;i++) {
        tcp_dns_job job;pthread_mutex_lock(&e->tcp->mutex);int ready=e->tcp->jobs[i].state==3;
        if(ready){job=e->tcp->jobs[i];e->tcp->jobs[i].state=0;}pthread_mutex_unlock(&e->tcp->mutex);if(!ready)continue;
        for(size_t j=0;j<TCP_MAX;j++) {tcp_session *s=&e->tcp->sessions[j];
            if(!s->used||s->phase!=4||strcmp(s->hex,job.id))continue;
            strcpy(s->ip,job.ip);
            if(!job.ok||!authorized(e,s)||connect_ip(s)){result(e,s,0);drop(e,s,0);}break;
        }
    }
}
/* Test seam also used after SO_ORIGINAL_DST: kernel TCP performs reassembly;
 * untrusted direct LAN connections to the listener cannot choose a target. */
int vr_tcp_attach(vr_engine *e,int fd,const char *ip,int port)
{
    tcp_session *s=allocate(e);if(!s)return -1;
    if(!public_ip(ip)||port<1||port>65535||vr_copy(s->ip,sizeof(s->ip),ip)){drop(e,s,0);return -1;}
    s->fd=fd;s->port=port;
    if(fcntl(fd,F_SETFL,fcntl(fd,F_GETFL,0)|O_NONBLOCK)<0){s->fd=-1;drop(e,s,0);return -1;}
    return 0;
}
size_t vr_tcp_pollfds(vr_engine *e,struct pollfd *fds)
{
    if(!e->tcp)return 0;
    fds[0]=(struct pollfd){e->tcp->listener,POLLIN,0};
    for(size_t i=0;i<TCP_MAX;i++) {
        tcp_session *s=&e->tcp->sessions[i];short local=0,up=0;
        if(s->used) {
            if(s->length[0]<TCP_BUFFER&&!s->closing)local|=POLLIN;
            if(s->length[1])local|=POLLOUT;
            if(s->phase==2)up=POLLOUT;
            if(s->phase==3) {if(s->length[0])up|=POLLOUT;if(s->length[1]<TCP_BUFFER&&!s->closing)up|=POLLIN;}
        }
        fds[1+i*2]=(struct pollfd){s->used?s->fd:-1,local,0};
        fds[2+i*2]=(struct pollfd){s->used?s->up:-1,up,0};
    }
    return 1+TCP_MAX*2;
}
static int write_buffer(int fd,uint8_t *buffer,size_t *length)
{
    if(!*length)return 0;
    ssize_t n=send(fd,buffer,*length,MSG_NOSIGNAL);
    if(n<0)return errno==EAGAIN||errno==EWOULDBLOCK||errno==EINTR?0:-1;
    if(n>0){memmove(buffer,buffer+n,*length-(size_t)n);*length-=(size_t)n;}
    return 0;
}
static int read_buffer(int fd,uint8_t *buffer,size_t *length)
{
    if(*length==TCP_BUFFER)return 0;
    ssize_t n=recv(fd,buffer+*length,TCP_BUFFER-*length,0);
    if(n<0)return errno==EAGAIN||errno==EWOULDBLOCK||errno==EINTR?0:-1;
    if(n==0)return -1;
    *length+=(size_t)n;return 0;
}
void vr_tcp_poll(vr_engine *e,struct pollfd *fds)
{
    if(!e->tcp)return;
    dns_results(e);
    if(fds[0].revents&POLLIN) {
        int fd=accept4(e->tcp->listener,NULL,NULL,SOCK_NONBLOCK|SOCK_CLOEXEC);
        if(fd>=0) {struct sockaddr_in original={0};socklen_t n=sizeof(original);char ip[16];
            if(getsockopt(fd,SOL_IP,ORIGINAL_DST,&original,&n)||original.sin_family!=AF_INET||
                !inet_ntop(AF_INET,&original.sin_addr,ip,sizeof(ip))||
                vr_tcp_attach(e,fd,ip,ntohs(original.sin_port)))close(fd);}
    }
    uint64_t now=vr_millis();
    for(size_t i=0;i<TCP_MAX;i++) {
        tcp_session *s=&e->tcp->sessions[i];if(!s->used)continue;
        short a=fds[1+i*2].fd==s->fd?fds[1+i*2].revents:0,b=fds[2+i*2].fd==s->up?fds[2+i*2].revents:0;
        if(now-s->active>120000||(s->phase!=3&&now-s->born>12000)){drop(e,s,1);continue;}
        if((a|b)&(POLLERR|POLLNVAL)){drop(e,s,1);continue;}
        if(s->phase==2&&(b&(POLLOUT|POLLHUP))) {
            int error=0;socklen_t n=sizeof(error);
            if(getsockopt(s->up,SOL_SOCKET,SO_ERROR,&error,&n)||error){if(s->exit_role)result(e,s,0);drop(e,s,1);continue;}
            s->phase=3;if(s->exit_role)result(e,s,1);
        }
        if(a&POLLOUT)if(write_buffer(s->fd,s->buffer[1],&s->length[1]))s->closing=1;
        if(s->phase==3&&(b&POLLOUT))if(write_buffer(s->up,s->buffer[0],&s->length[0]))s->closing=1;
        if((a&(POLLIN|POLLHUP))&&!s->closing) {size_t before=s->length[0];if(read_buffer(s->fd,s->buffer[0],&s->length[0]))s->closing=1;if(s->length[0]!=before)s->active=now;}
        if(s->phase==3&&(b&(POLLIN|POLLHUP))&&!s->closing) {size_t before=s->length[1];if(read_buffer(s->up,s->buffer[1],&s->length[1]))s->closing=1;if(s->length[1]!=before)s->active=now;}
        if(!s->phase&&(vr_tls_server_name(s->buffer[0],s->length[0],s->domain)||now-s->born>=2000))decide(e,s);
        if(!s->used)continue;
        size_t lane=s->exit_role?1:0;
        if(s->remote&&s->phase==3&&s->length[lane]) {
            uint8_t payload[32784];size_t n=s->length[lane];if(n>32768)n=32768;
            memcpy(payload,s->id,16);memcpy(payload+16,s->buffer[lane],n);
            if(!vr_send(e,8,3,s->path,s->count,payload,n+16)) {memmove(s->buffer[lane],s->buffer[lane]+n,s->length[lane]-n);s->length[lane]-=n;s->active=now;}
        }
        if(s->closing&&!s->length[0]&&!s->length[1])drop(e,s,1);
    }
}

void vr_tcp_frame(vr_engine *e,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count)
{
    if(count<2||count>VR_PATH_MAX||strcmp(path[count-1],e->config.node_id))return;
    uint8_t id[16];char hex[33]={0};json_object *o=NULL;
    if(f->sub==3) {if(f->data_len<=16)return;memcpy(id,f->data,16);}
    else {if(f->data_len>4096)return;o=vr_json_parse(f->data,f->data_len);const char *raw=vr_string(o,"session_id");
        if(hex_id(raw,id)){json_object_put(o);return;}strcpy(hex,raw);}
    tcp_session *s=NULL;
    if(e->tcp)for(size_t i=0;i<TCP_MAX;i++)if(e->tcp->sessions[i].used&&!memcmp(e->tcp->sessions[i].id,id,16)){s=&e->tcp->sessions[i];break;}
    if(s) {
        /* Session IDs are not authorization: bind every frame to its complete
         * authenticated reverse path, including the original endpoint. */
        if(s->count!=count)goto done;
        for(size_t i=0;i<count;i++)if(strcmp(path[i],s->path[count-i-1]))goto done;
        if(f->sub==2&&!s->exit_role&&s->phase==1) {if(json_object_get_boolean(vr_member(o,"ok")))s->phase=3;else drop(e,s,0);}
        else if(f->sub==3&&s->phase==3) {
            size_t lane=s->exit_role?0:1,len=f->data_len-16;
            if(len>TCP_BUFFER-s->length[lane])drop(e,s,1);
            else {memcpy(s->buffer[lane]+s->length[lane],f->data+16,len);s->length[lane]+=len;s->active=vr_millis();}
        } else if(f->sub==4)s->closing=1;
        goto done;
    }
    if(f->sub!=1)goto done;
    if(strcmp(vr_string(o,"source_node_id"),path[0])||strcmp(vr_string(o,"exit_node_id"),e->config.node_id))goto done;
    s=allocate(e);if(!s)goto done;
    s->remote=s->exit_role=1;memcpy(s->id,id,16);strcpy(s->hex,hex);s->count=count;
    for(size_t i=0;i<count;i++)strcpy(s->path[i],path[count-i-1]);
    char target[280];if(vr_copy(target,sizeof(target),vr_string(o,"target_addr")))goto reject;
    char *colon=strrchr(target,':'),*end;if(!colon)goto reject;*colon++=0;long port=strtol(colon,&end,10);
    if(!*colon||*end||port<1||port>65535)goto reject;
    s->port=(int)port;struct in_addr numeric;
    if(inet_pton(AF_INET,target,&numeric)==1) {
        strcpy(s->ip,target);if(!authorized(e,s)||connect_ip(s))goto reject;
    } else {
        char host[254];int parsed_port;
        if(vr_dns_endpoint(vr_string(o,"target_addr"),host,&parsed_port,0)||vr_copy(s->domain,sizeof(s->domain),host))goto reject;
        if(resolve_async(e,s))goto reject;
    }
    goto done;
reject:result(e,s,0);drop(e,s,0);
done:json_object_put(o);
}
void vr_tcp_stop(vr_engine *e)
{
    if(!e->tcp)return;
    if(e->tcp->started) {pthread_mutex_lock(&e->tcp->mutex);e->tcp->stop=1;pthread_cond_signal(&e->tcp->changed);pthread_mutex_unlock(&e->tcp->mutex);pthread_join(e->tcp->thread,NULL);}
    if(e->tcp->listener>=0)close(e->tcp->listener);
    for(size_t i=0;i<TCP_MAX;i++)drop(e,&e->tcp->sessions[i],0);
    pthread_cond_destroy(&e->tcp->changed);pthread_mutex_destroy(&e->tcp->mutex);free(e->tcp);e->tcp=NULL;
}
