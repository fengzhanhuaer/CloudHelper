#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <curl/curl.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <netinet/tcp.h>
#include <unistd.h>

#ifndef IP_FREEBIND
#define IP_FREEBIND 15
#endif
#ifndef IP_TRANSPARENT
#define IP_TRANSPARENT 19
#endif
#define EXTERNAL_BYTES (64ull*1024*1024)
#define EXTERNAL_MS 10000u

struct vr_external_state {
    vr_engine *engine;pthread_t thread;vr_config config;
    char node[129],iface[16],own[16],real[16],fake[16];
    uint32_t source,destination,resolved;uint16_t port;
    int done,stop,fd,active;uint64_t start,first,last,bytes,published;
    long status;CURL *curl;uint8_t tcp_info[256];size_t tcp_info_length;uint64_t tcp_info_at;
};
static int cancelled(struct vr_external_state *x)
{
    pthread_mutex_lock(&x->engine->service->mutex);int stop=x->engine->service->measure_cancel;pthread_mutex_unlock(&x->engine->service->mutex);
    pthread_mutex_lock(&x->engine->mutex);stop|=x->stop;pthread_mutex_unlock(&x->engine->mutex);return stop;
}
static void publish(struct vr_external_state *x,int running,const char *error,int stopped)
{
    if(x->fd>=0){socklen_t length=sizeof(x->tcp_info);if(!getsockopt(x->fd,IPPROTO_TCP,TCP_INFO,x->tcp_info,&length)){x->tcp_info_length=length<=sizeof(x->tcp_info)?length:sizeof(x->tcp_info);x->tcp_info_at=vr_millis();}}
    vr_service *s=x->engine->service;json_object *job=json_object_new_object(),*r=json_object_new_object();
    uint64_t elapsed=x->last>x->first?x->last-x->first:x->bytes?1:0;
    double mbps=elapsed?(double)x->bytes*8/(double)elapsed/1000:0;
    json_object_object_add(job,"running",json_object_new_boolean(running));json_object_object_add(job,"kind",json_object_new_string("external"));
    json_object_object_add(r,"ok",json_object_new_boolean(!running&&!error&&x->bytes>0));
    json_object_object_add(r,"cancelled",json_object_new_boolean(stopped));
    json_object_object_add(r,"mode",json_object_new_string(*x->node?"node":"direct"));
    json_object_object_add(r,"node_id",json_object_new_string(x->node));
    json_object_object_add(r,"source",json_object_new_string("Cloudflare"));
    json_object_object_add(r,"real_ip",json_object_new_string(x->real));
    json_object_object_add(r,"bytes",json_object_new_int64((int64_t)x->bytes));
    json_object_object_add(r,"duration_ms",json_object_new_int64((int64_t)elapsed));
    json_object_object_add(r,"total_ms",json_object_new_int64((int64_t)(vr_millis()-x->start)));
    json_object_object_add(r,"mbps",json_object_new_double(mbps));
    json_object_object_add(r,"mb_per_second",json_object_new_double(mbps/8));
    json_object_object_add(r,"tcp_info",vr_tcp_info_decode(x->tcp_info,x->tcp_info_length));
    json_object_object_add(r,"tcp_info_age_ms",x->tcp_info_at?json_object_new_int64((int64_t)(vr_millis()-x->tcp_info_at)):NULL);
    json_object_object_add(r,"http_status",json_object_new_int((int)x->status));
    if(error)json_object_object_add(r,"error",json_object_new_string(error));
    json_object_object_add(job,"result",r);pthread_mutex_lock(&s->mutex);json_object_put(s->measurement);s->measurement=job;pthread_mutex_unlock(&s->mutex);
}
static size_t receive_data(char *data,size_t size,size_t count,void *opaque)
{
    (void)data;struct vr_external_state *x=opaque;size_t n=size*count;
    curl_easy_getinfo(x->curl,CURLINFO_RESPONSE_CODE,&x->status);
    if(x->status!=200||cancelled(x))return 0;
    uint64_t now=vr_millis();if(x->first&&now-x->first>=EXTERNAL_MS)return 0;
    if(!x->first)x->first=now;
    x->last=now;
    uint64_t remaining=EXTERNAL_BYTES-x->bytes;x->bytes+=n>remaining?remaining:n;
    if(now-x->published>=250){publish(x,1,NULL,0);x->published=now;}
    return x->bytes>=EXTERNAL_BYTES?0:n;
}
static int progress(void *opaque,curl_off_t total,curl_off_t received,curl_off_t up_total,curl_off_t uploaded)
{
    (void)total;(void)received;(void)up_total;(void)uploaded;struct vr_external_state *x=opaque;
    return cancelled(x)||(x->first&&vr_millis()-x->first>=EXTERNAL_MS);
}
static curl_socket_t open_socket(void *opaque,curlsocktype purpose,struct curl_sockaddr *address)
{
    (void)purpose;struct vr_external_state *x=opaque;
    if(address->family!=AF_INET||address->socktype!=SOCK_STREAM)return CURL_SOCKET_BAD;
    int fd=socket(AF_INET,SOCK_STREAM|SOCK_CLOEXEC,0);if(fd<0)return CURL_SOCKET_BAD;
    int on=1;struct sockaddr_in local={.sin_family=AF_INET,.sin_addr={x->source}};
    /* FREEBIND permits bind(), while TRANSPARENT also permits routing packets
       whose source is the unassigned virtual IP. Both are socket-local. */
    if(*x->node&&(setsockopt(fd,IPPROTO_IP,IP_FREEBIND,&on,sizeof(on))||setsockopt(fd,IPPROTO_IP,IP_TRANSPARENT,&on,sizeof(on))||bind(fd,(struct sockaddr *)&local,sizeof(local)))){close(fd);return CURL_SOCKET_BAD;}
    socklen_t length=sizeof(local);if(getsockname(fd,(struct sockaddr *)&local,&length)){close(fd);return CURL_SOCKET_BAD;}
    pthread_mutex_lock(&x->engine->mutex);x->fd=fd;x->port=ntohs(local.sin_port);x->active=!!*x->node;pthread_mutex_unlock(&x->engine->mutex);return fd;
}
static int close_socket(void *opaque,curl_socket_t fd)
{
    struct vr_external_state *x=opaque;pthread_mutex_lock(&x->engine->mutex);if(x->fd==fd){x->fd=-1;x->active=0;}pthread_mutex_unlock(&x->engine->mutex);return close(fd);
}
static void *worker(void *opaque)
{
    struct vr_external_state *x=opaque;const char *error=NULL;CURLcode rc=CURLE_FAILED_INIT;
    struct curl_slist *resolve=NULL,*headers=NULL;x->start=vr_millis();
    if(cancelled(x))goto done;
    if(vr_resolve_config4("speed.cloudflare.com",&x->config,x->iface,x->real)||vr_public_ip(x->real,AF_INET,x->real,sizeof(x->real))){error="测速源没有可用公网 IPv4";goto done;}
    if(cancelled(x))goto done;
    x->resolved=inet_addr(x->real);x->curl=curl_easy_init();if(!x->curl){error="无法创建测速连接";goto done;}
    char entry[128];snprintf(entry,sizeof(entry),"speed.cloudflare.com:443:%s",*x->node?x->fake:x->real);resolve=curl_slist_append(resolve,entry);
    headers=curl_slist_append(headers,"Accept-Encoding: identity");headers=curl_slist_append(headers,"Cache-Control: no-cache");
    curl_easy_setopt(x->curl,CURLOPT_URL,"https://speed.cloudflare.com/__down?bytes=67108864");
    curl_easy_setopt(x->curl,CURLOPT_RESOLVE,resolve);curl_easy_setopt(x->curl,CURLOPT_HTTPHEADER,headers);
    curl_easy_setopt(x->curl,CURLOPT_PROXY,"");curl_easy_setopt(x->curl,CURLOPT_FOLLOWLOCATION,0L);
    curl_easy_setopt(x->curl,CURLOPT_PROTOCOLS,(long)CURLPROTO_HTTPS);curl_easy_setopt(x->curl,CURLOPT_HTTP_VERSION,CURL_HTTP_VERSION_1_1);
    curl_easy_setopt(x->curl,CURLOPT_IPRESOLVE,CURL_IPRESOLVE_V4);curl_easy_setopt(x->curl,CURLOPT_CONNECTTIMEOUT_MS,8000L);
    curl_easy_setopt(x->curl,CURLOPT_TIMEOUT_MS,25000L);curl_easy_setopt(x->curl,CURLOPT_NOSIGNAL,1L);
    curl_easy_setopt(x->curl,CURLOPT_SSL_VERIFYPEER,1L);curl_easy_setopt(x->curl,CURLOPT_SSL_VERIFYHOST,2L);
    const char *ca=getenv("SSL_CERT_FILE");if(ca&&*ca)curl_easy_setopt(x->curl,CURLOPT_CAINFO,ca);
    curl_easy_setopt(x->curl,CURLOPT_WRITEFUNCTION,receive_data);curl_easy_setopt(x->curl,CURLOPT_WRITEDATA,x);
    curl_easy_setopt(x->curl,CURLOPT_NOPROGRESS,0L);curl_easy_setopt(x->curl,CURLOPT_XFERINFOFUNCTION,progress);curl_easy_setopt(x->curl,CURLOPT_XFERINFODATA,x);
    curl_easy_setopt(x->curl,CURLOPT_OPENSOCKETFUNCTION,open_socket);curl_easy_setopt(x->curl,CURLOPT_OPENSOCKETDATA,x);curl_easy_setopt(x->curl,CURLOPT_CLOSESOCKETFUNCTION,close_socket);curl_easy_setopt(x->curl,CURLOPT_CLOSESOCKETDATA,x);
    if(!*x->node&&*x->iface)curl_easy_setopt(x->curl,CURLOPT_INTERFACE,x->iface);
    rc=curl_easy_perform(x->curl);curl_easy_getinfo(x->curl,CURLINFO_RESPONSE_CODE,&x->status);
    if(x->status!=200)error="测速源未返回 HTTP 200";
    else if(rc!=CURLE_OK&&x->bytes<EXTERNAL_BYTES&&(!x->first||vr_millis()-x->first<EXTERNAL_MS))error=curl_easy_strerror(rc);
    else if(!x->bytes)error="未收到测速数据";
done:
    if(x->curl){curl_easy_cleanup(x->curl);x->curl=NULL;}curl_slist_free_all(resolve);curl_slist_free_all(headers);
    int stopped=cancelled(x);publish(x,0,stopped?"测速已停止":error,stopped);
    pthread_mutex_lock(&x->engine->mutex);x->done=1;pthread_mutex_unlock(&x->engine->mutex);return NULL;
}
void vr_external_tick(vr_engine *e)
{
    pthread_mutex_lock(&e->mutex);struct vr_external_state *x=e->external;int done=x&&x->done;pthread_mutex_unlock(&e->mutex);
    if(done){pthread_join(x->thread,NULL);pthread_mutex_lock(&e->mutex);e->external=NULL;pthread_mutex_unlock(&e->mutex);memset(x,0,sizeof(*x));free(x);}
}
int vr_external_start(vr_engine *e,const char *node)
{
    vr_external_tick(e);if(e->external)return -1;
    struct vr_external_state *x=calloc(1,sizeof(*x));if(!x)return -1;x->engine=e;x->config=e->config;x->fd=-1;
    if(strcmp(node,"direct")){strcpy(x->node,node);char path[VR_PATH_MAX][129];size_t n;if(!strcmp(node,e->config.node_id)||vr_path(e,node,path,&n)||n<2){free(x);return -1;}}
    strcpy(x->iface,e->addresses.wan_if);strcpy(x->own,e->own_ip);x->source=inet_addr(x->own);
    /* A private test destination inside the already installed TUN prefix. Only
       the source port owned by this job is intercepted; no fake DNS mapping. */
    char cidr[64];strcpy(cidr,e->cidr);char *slash=strchr(cidr,'/');if(!slash){free(x);return -1;}*slash++=0;int bits=atoi(slash);struct in_addr base;
    if(bits<8||bits>30||inet_pton(AF_INET,cidr,&base)!=1){free(x);return -1;}
    uint32_t mask=0xffffffffu<<(32-bits);struct in_addr fake={htonl((ntohl(base.s_addr)&mask)|(~mask-1))};
    x->destination=fake.s_addr;inet_ntop(AF_INET,&fake,x->fake,sizeof(x->fake));if(x->destination==x->source){free(x);return -1;}
    pthread_mutex_lock(&e->mutex);e->external=x;pthread_mutex_unlock(&e->mutex);
    if(pthread_create(&x->thread,NULL,worker,x)){pthread_mutex_lock(&e->mutex);e->external=NULL;pthread_mutex_unlock(&e->mutex);free(x);return -1;}return 0;
}
int vr_external_target(vr_engine *e,const vr_packet *p,uint32_t *real,char node[129])
{
    pthread_mutex_lock(&e->mutex);struct vr_external_state *x=e->external;
    int match=x&&*x->node&&x->active&&!x->done&&p->protocol==6&&p->src==x->source&&p->dst==x->destination&&p->sport==x->port&&p->dport==443;
    if(match){*real=x->resolved;strcpy(node,x->node);}pthread_mutex_unlock(&e->mutex);return match;
}
void vr_external_stop(vr_engine *e)
{
    pthread_mutex_lock(&e->mutex);struct vr_external_state *x=e->external;if(x){x->stop=1;if(x->fd>=0)shutdown(x->fd,SHUT_RDWR);}pthread_mutex_unlock(&e->mutex);
    if(x){pthread_join(x->thread,NULL);pthread_mutex_lock(&e->mutex);e->external=NULL;pthread_mutex_unlock(&e->mutex);memset(x,0,sizeof(*x));free(x);}
}
