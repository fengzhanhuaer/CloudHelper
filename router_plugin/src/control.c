#define _GNU_SOURCE
#include "engine.h"
#include "upgrade.h"
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/sysinfo.h>
#include <unistd.h>
#include <openssl/rand.h>
#include <openssl/err.h>
#include <curl/curl.h>

/* The existing controller carries one JSON control stream over yamux/WS.
 * Implement only that bounded stream; no general mux runtime or new library. */
typedef struct {
    vr_engine *engine;SSL *ssl;int fd,refresh,report;
    vr_ws_decoder ws;
    uint8_t header[12];size_t header_used,payload_left;
    uint32_t send_window;size_t message_used;
    char endpoint[280];char message[16384];uint64_t ack_at,started;int direct;
    char *pending;size_t pending_len,pending_at;unsigned reset_epoch;int waiting_reset;
} control;

static uint32_t u32(const uint8_t *b){return ((uint32_t)b[0]<<24)|((uint32_t)b[1]<<16)|((uint32_t)b[2]<<8)|b[3];}
static void p32(uint8_t *b,uint32_t n){b[0]=(uint8_t)(n>>24);b[1]=(uint8_t)(n>>16);b[2]=(uint8_t)(n>>8);b[3]=(uint8_t)n;}

static int stopped(vr_engine *e)
{
    pthread_mutex_lock(&e->mutex);int stop=e->control_stop;pthread_mutex_unlock(&e->mutex);
    return stop||vr_service_stopped(e->service,e->generation);
}

typedef struct {char text[64];size_t used;} ip_response;
static size_t ip_received(char *data,size_t size,size_t count,void *arg)
{
    ip_response *r=arg;if(size&&count>63/size)return 0;size_t n=size*count;
    if(n>sizeof(r->text)-1-r->used)return 0;
    memcpy(r->text+r->used,data,n);r->used+=n;r->text[r->used]=0;return n;
}
static int ip_cancel(void *arg,curl_off_t a,curl_off_t b,curl_off_t d,curl_off_t f)
{(void)a;(void)b;(void)d;(void)f;return stopped(arg);}
static int public_ip(vr_engine *e,int family,char *out,size_t cap)
{
    vr_addresses addresses=vr_engine_addresses(e);
    CURL *curl=curl_easy_init();if(!curl)return -1;ip_response response={{0},0};long status=0;
    const char *urls4[]={"https://api4.ipify.org","https://ipv4.icanhazip.com"};
    const char *urls6[]={"https://api6.ipify.org","https://ipv6.icanhazip.com"};
    curl_easy_setopt(curl,CURLOPT_PROXY,"");curl_easy_setopt(curl,CURLOPT_INTERFACE,addresses.wan_if);
    curl_easy_setopt(curl,CURLOPT_IPRESOLVE,family==AF_INET?CURL_IPRESOLVE_V4:CURL_IPRESOLVE_V6);
    curl_easy_setopt(curl,CURLOPT_CONNECTTIMEOUT_MS,2000L);curl_easy_setopt(curl,CURLOPT_TIMEOUT_MS,3000L);curl_easy_setopt(curl,CURLOPT_NOSIGNAL,1L);
    curl_easy_setopt(curl,CURLOPT_WRITEFUNCTION,ip_received);curl_easy_setopt(curl,CURLOPT_WRITEDATA,&response);
    curl_easy_setopt(curl,CURLOPT_NOPROGRESS,0L);curl_easy_setopt(curl,CURLOPT_XFERINFOFUNCTION,ip_cancel);curl_easy_setopt(curl,CURLOPT_XFERINFODATA,e);
    int result=-1;
    for(unsigned i=0;i<2&&!stopped(e);i++) {
        memset(&response,0,sizeof(response));curl_easy_setopt(curl,CURLOPT_URL,family==AF_INET?urls4[i]:urls6[i]);
        struct curl_slist *resolve=NULL;
        if(family==AF_INET) {
            char host[254],ips[4][16],entry[320];int port,tls;
            /* Ordinary router DNS may return Fake IPs. Reuse the controller's
               WAN-bound upstream resolution instead of recursing into dnsmasq. */
            if(vr_origin(urls4[i],host,&port,&tls)||vr_resolve_config4_all(host,&e->config,addresses.wan_if,ips)<1)continue;
            snprintf(entry,sizeof(entry),"%s:%d:%s",host,port,ips[0]);resolve=curl_slist_append(NULL,entry);
            if(!resolve)continue;
            curl_easy_setopt(curl,CURLOPT_RESOLVE,resolve);
        }
        CURLcode rc=curl_easy_perform(curl);curl_easy_getinfo(curl,CURLINFO_RESPONSE_CODE,&status);
        curl_easy_setopt(curl,CURLOPT_RESOLVE,NULL);curl_slist_free_all(resolve);
        if(rc==CURLE_OK&&status==200&&!vr_public_ip(response.text,family,out,cap)){result=0;break;}
    }
    curl_easy_cleanup(curl);return result;
}
static void *public_ip_worker(void *arg)
{
    vr_engine *e=arg;uint64_t refresh_at=0,last4=0,last6=0;
    while(!stopped(e)) {
        uint64_t now=vr_millis();
        if(now>=refresh_at) {
            json_object *o=json_object_new_object();json_object_object_add(o,"ipv4",json_object_new_array());
            int has6=vr_report_addresses(o,"","");char global6[46]="";
            if(has6)vr_copy(global6,sizeof(global6),json_object_get_string(json_object_array_get_idx(vr_member(o,"ipv6"),0)));
            json_object_put(o);
            char v4[16]="",v6[46]="";int ok4=!public_ip(e,AF_INET,v4,sizeof(v4)),ok6=has6&&!stopped(e)&&!public_ip(e,AF_INET6,v6,sizeof(v6));
            /* A globally routed interface address is already a valid public
               IPv6 address even if the external reflector is unreachable. */
            if(has6&&!ok6){vr_copy(v6,sizeof(v6),global6);ok6=1;}
            if(ok4)last4=now;
            if(ok6)last6=now;
            pthread_mutex_lock(&e->mutex);
            if(ok4)vr_copy(e->public_ipv4,sizeof(e->public_ipv4),v4);else if(now-last4>=1800000)e->public_ipv4[0]=0;
            if(ok6)vr_copy(e->public_ipv6,sizeof(e->public_ipv6),v6);else if(!has6||now-last6>=1800000)e->public_ipv6[0]=0;
            pthread_mutex_unlock(&e->mutex);refresh_at=now+(ok4?300000:60000);
        }
        poll(NULL,0,100);
    }
    return NULL;
}

static int send_ws(control *c,int opcode,const void *bytes,size_t n)
{
    if(n>16384)return -1;
    uint8_t *out=malloc(n+14);size_t length;int rc=-1;if(!out)return -1;
    if(!vr_ws_encode(opcode,1,bytes,n,out,n+14,&length))rc=vr_tls_io(c->ssl,1,out,length,vr_millis()+3000);
    free(out);return rc;
}

static int send_mux(control *c,int type,int flags,uint32_t stream,uint32_t length,const void *data)
{
    uint8_t wire[16384];size_t n=12+(type==0?length:0);
    if(n>sizeof(wire)||(type==0&&length>c->send_window))return -1;
    wire[0]=0;wire[1]=(uint8_t)type;wire[2]=0;wire[3]=(uint8_t)flags;p32(wire+4,stream);p32(wire+8,length);
    if(type==0){memcpy(wire+12,data,length);c->send_window-=length;}
    return send_ws(c,2,wire,n);
}

static int message(control *c)
{
    json_object *m=vr_json_parse(c->message,c->message_used);c->message_used=0;if(!m)return -1;
    const char *type=vr_string(m,"type");
    if(!strcmp(type,"ack")){c->ack_at=vr_millis();pthread_mutex_lock(&c->engine->mutex);c->engine->controller_connected=1;vr_copy(c->engine->controller_endpoint,sizeof(c->engine->controller_endpoint),c->endpoint);c->engine->controller_direct=c->direct;pthread_mutex_unlock(&c->engine->mutex);}
    else if(!strcmp(type,"route_config_sync"))c->refresh=1;
    else if(!strcmp(type,"report_once"))c->report=1;
    else if(!strcmp(type,"virtual_router_buffers_reset")) {
        pthread_mutex_lock(&c->engine->mutex);c->reset_epoch=c->engine->telemetry_epoch+1;c->engine->buffers_reset=1;pthread_mutex_unlock(&c->engine->mutex);c->waiting_reset=1;
    }
    else if(!strcmp(type,"upgrade")&&!strcmp(vr_string(m,"build_kind"),"asus_jffs")) {
        char err[256]="cannot start upgrade";
        if(vr_upgrade_start(c->engine->service,m,err,sizeof(err)))vr_service_log(c->engine->service,"upgrade",err);
    }
    json_object_put(m);return 0;
}

static int received(int opcode,const uint8_t *data,size_t n,void *arg)
{
    control *c=arg;
    if(opcode==9)return send_ws(c,10,data,n);
    if(opcode==10)return 0;
    if(opcode!=2)return -1;
    size_t offset=0;
    while(offset<n) {
        if(c->header_used<12) {
            size_t take=12-c->header_used;if(take>n-offset)take=n-offset;
            memcpy(c->header+c->header_used,data+offset,take);c->header_used+=take;offset+=take;
            if(c->header_used<12)continue;
            uint8_t *h=c->header;uint32_t stream=u32(h+4),length=u32(h+8);unsigned flags=((unsigned)h[2]<<8)|h[3];
            if(h[0]||h[1]>3||(flags&~15u))return -1;
            if(h[1]==2) {
                if(stream || (flags!=1&&flags!=2))return -1;
                if(flags==1 && send_mux(c,2,2,0,length,NULL))return -1;
                c->header_used=0;continue;
            }
            if(h[1]==3||stream!=1||(flags&(1|4|8))){char detail[120];snprintf(detail,sizeof(detail),"Control mux closed: type %u stream %u flags %u",h[1],stream,flags);vr_service_log(c->engine->service,"controller",detail);return -1;}
            if(h[1]==1) {
                if(length>16*1024*1024u || c->send_window>16*1024*1024u-length)return -1;
                c->send_window+=length;c->header_used=0;continue;
            }
            if(length>256*1024)return -1;
            c->payload_left=length;
            if(!length){c->header_used=0;continue;}
        }
        size_t take=c->payload_left;if(take>n-offset)take=n-offset;
        for(size_t i=0;i<take;i++) {
            uint8_t b=data[offset+i];
            if(b=='\n'){if(c->message_used&&message(c))return -1;continue;}
            if(!b || c->message_used>=sizeof(c->message))return -1;
            c->message[c->message_used++]=(char)b;
        }
        offset+=take;c->payload_left-=take;
        if(!c->payload_left) {
            uint32_t length=u32(c->header+8);c->header_used=0;
            if(send_mux(c,1,0,1,length,NULL))return -1;
        }
    }
    return 0;
}

static int connect_controller(control *c)
{
    vr_engine *e=c->engine;vr_addresses addresses=vr_engine_addresses(e);char challenge[513],signature[65],host[254],key[25],accept[29],actual[64],origin[1024],pin[65];int port,tls;
    char error[128]="invalid controller URL";
    if(vr_origin(e->config.controller,host,&port,&tls)||!tls||
       vr_controller_auth(&e->config,addresses.wan_if,"GET","/api/probe",challenge,signature,error,sizeof(error))||stopped(e)){
        if(!stopped(e))vr_service_log(e->service,"controller",error);
        return -1;
    }
    uint64_t deadline=vr_millis()+5000;
    c->direct=vr_controller_link_target(&e->config,origin,pin);c->started=vr_millis();
    if(vr_origin(origin,host,&port,&tls)||!tls)return -1;
    snprintf(c->endpoint,sizeof(c->endpoint),"%s:%d",host,port);
    c->ssl=vr_tls_connect(host,port,pin,&e->config,addresses.wan_if,&c->fd,deadline);if(!c->ssl){vr_controller_link_feedback(&e->config,c->direct,0,vr_millis()-c->started);vr_service_log(e->service,"controller","Control stream DNS/TCP/TLS connection failed");return -1;}
    uint8_t random[16];if(RAND_bytes(random,16)!=1)return -1;EVP_EncodeBlock((unsigned char *)key,random,16);
    if(vr_ws_accept_key(key,accept))return -1;
    char header[4096];int len=snprintf(header,sizeof(header),"GET /api/probe HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nX-Probe-Node-Id: %s\r\nX-Probe-Rand: %s\r\nX-Probe-Signature: %s\r\n\r\n",host,port,key,e->config.node_id,challenge,signature);
    if(len<0||len>=(int)sizeof(header)||vr_tls_io(c->ssl,1,header,(size_t)len,deadline)||vr_http_header(c->ssl,header,sizeof(header),deadline)||
       strncmp(header,"HTTP/1.1 101 ",13)||!vr_http_token(header,"Connection","Upgrade")||!vr_http_token(header,"Upgrade","websocket")||
       !vr_http_field(header,"Sec-WebSocket-Accept",actual,sizeof(actual))||strcmp(accept,actual))return -1;
    c->send_window=256*1024;c->ack_at=vr_millis();return send_mux(c,1,1,1,0,NULL);
}

static int report(control *c)
{
    vr_engine *e=c->engine;vr_addresses addresses=vr_engine_addresses(e);json_object *o=vr_probe_report(&e->config,&addresses,"/sys/class/net");
    char v4[16],v6[46];pthread_mutex_lock(&e->mutex);
    /* Old firmware json-c may not have atomic reference counts. Copy while
       locked instead of sharing JSON objects across threads. */
    const char *wire=e->telemetry?json_object_to_json_string_ext(e->telemetry,JSON_C_TO_STRING_PLAIN):NULL;
    char *copy=wire&&strlen(wire)<256*1024?strdup(wire):NULL;
    vr_copy(v4,sizeof(v4),e->public_ipv4);vr_copy(v6,sizeof(v6),e->public_ipv6);pthread_mutex_unlock(&e->mutex);
    vr_report_addresses(o,v4,v6);
    json_object *snapshot=copy?vr_json_parse(copy,strlen(copy)):NULL;free(copy);
    if(snapshot){json_object_object_foreach(snapshot,key,value){json_object_object_add(o,key,json_object_get(value));}json_object_put(snapshot);}
    const char *text=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);size_t len=strlen(text);int rc=-1;
    if(len<256*1024&& (c->pending=malloc(len+1))){memcpy(c->pending,text,len);c->pending[len]='\n';c->pending_len=len+1;c->pending_at=0;rc=0;}
    json_object_put(o);return rc;
}

static int flush_report(control *c)
{
    /* Leave time to read ACKs/window updates even on a slow controller. */
    for(int i=0;i<2&&c->pending&&c->send_window;i++) {
        size_t n=c->pending_len-c->pending_at;if(n>8192)n=8192;if(n>c->send_window)n=c->send_window;
        if(send_mux(c,0,0,1,(uint32_t)n,c->pending+c->pending_at))return -1;
        c->pending_at+=n;if(c->pending_at==c->pending_len){free(c->pending);c->pending=NULL;c->pending_at=c->pending_len=0;}
    }
    return 0;
}

static int refresh(control *c)
{
    vr_engine *e=c->engine;vr_addresses addresses=vr_engine_addresses(e);char error[256];
    json_object *next=vr_controller_call(&e->config,addresses.wan_if,"GET","/api/probe/route/config",NULL,error,sizeof(error));
    if(!next){vr_service_log(e->service,"controller","Configuration refresh failed; retaining current policy and control stream");return -1;}
    if(vr_policy_validate(next,e->config.node_id,error,sizeof(error))){vr_service_log(e->service,"controller",error);json_object_put(next);return -1;}
    char path[512];
    if(vr_copy(path,sizeof(path),e->service->config_path)){json_object_put(next);return -1;}
    char *slash=strrchr(path,'/');
    if(!slash || (size_t)(slash-path)+sizeof("/route-cache.json")>sizeof(path)){json_object_put(next);return -1;}
    strcpy(slash,"/route-cache.json");
    pthread_mutex_lock(&e->mutex);
    int changed=!vr_policy_equal(e->next_policy?e->next_policy:e->snapshot,next);
    pthread_mutex_unlock(&e->mutex);
    if(changed) {
        if(vr_service_stopped(e->service,e->generation)||vr_cache_save(path,next)) {
            vr_service_log(e->service,"controller","Cannot persist route configuration; retaining current policy and links");
            json_object_put(next);return -1;
        }
        pthread_mutex_lock(&e->mutex);
        json_object_put(e->next_policy);e->next_policy=next;e->policy_retry=0;next=NULL;
        pthread_mutex_unlock(&e->mutex);
    }
    json_object_put(next);return 0;
}

static void *control_worker(void *arg)
{
    vr_engine *e=arg;
    while(!stopped(e)) {
        control *c=calloc(1,sizeof(*c));if(!c)break;c->engine=e;c->fd=-1;
        if(!connect_controller(c)) {
            vr_service_log(e->service,"controller",c->direct?"Control stream established via pinned origin IP":"Control stream established via primary domain");uint64_t sent=0,next_check=vr_millis()+60000;unsigned failures=0;
            while(!stopped(e)) {
                uint64_t now=vr_millis();
                if(now-c->ack_at>65000)break;
                if(c->waiting_reset){pthread_mutex_lock(&e->mutex);int done=e->telemetry_epoch>=c->reset_epoch;pthread_mutex_unlock(&e->mutex);if(done){c->waiting_reset=0;c->report=1;}}
                if(!c->pending&&!c->waiting_reset&&(c->report||now-sent>=20000)){if(report(c))break;sent=now;c->report=0;}
                if(flush_report(c))break;
                if(c->refresh||now>=next_check){int rc=refresh(c);c->refresh=0;
                    if(rc<0){if(failures<4)failures++;unsigned delay=failures==4?30u:5u<<(failures-1);next_check=vr_millis()+delay*1000;}
                    else{failures=0;next_check=vr_millis()+60000;}}
                struct pollfd fd={c->fd,POLLIN,0};int rc=poll(&fd,1,100);
                if(rc<0&&errno==EINTR)continue;
                if(rc<0||(fd.revents&(POLLERR|POLLHUP|POLLNVAL)))break;
                if((fd.revents&POLLIN)||SSL_pending(c->ssl)) {
                    ERR_clear_error();errno=0;
                    uint8_t buf[4096];int n=SSL_read(c->ssl,buf,sizeof(buf));
                    if(n>0){if(vr_ws_feed(&c->ws,buf,(size_t)n,received,c)){vr_service_log(e->service,"controller","Control WebSocket/mux decode failed");break;}}
                    else {int err=SSL_get_error(c->ssl,n);if(err!=SSL_ERROR_WANT_READ&&err!=SSL_ERROR_WANT_WRITE){char detail[120];vr_tls_error(detail,sizeof(detail),"control receive",err);vr_service_log(e->service,"controller",detail);break;}}
                }
            }
        }
        if(c->ssl&&!stopped(e))vr_service_log(e->service,"controller","Control stream disconnected or handshake failed; retrying");
          pthread_mutex_lock(&e->mutex);int intentional=e->refresh;pthread_mutex_unlock(&e->mutex);
          if(c->started&&!stopped(e)&&!intentional)vr_controller_link_feedback(&e->config,c->direct,vr_millis()-c->started>=90000,vr_millis()-c->started>=90000?1000:3000);
          SSL_free(c->ssl);if(c->fd>=0)close(c->fd);free(c->pending);free(c);
        pthread_mutex_lock(&e->mutex);e->controller_connected=0;e->controller_endpoint[0]=0;e->controller_direct=0;int changed=e->refresh;pthread_mutex_unlock(&e->mutex);if(changed)break;
        for(int i=0;i<50&&!stopped(e);i++)poll(NULL,0,100);
    }
    return NULL;
}

int vr_control_start(vr_engine *e)
{
    vr_addresses addresses=vr_engine_addresses(e);
    pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,256*1024);
    int rc=pthread_create(&e->control_thread,&attr,control_worker,e);
    if(!rc)e->control_started=1;
    if(!rc&&*addresses.wan_if&&!pthread_create(&e->public_ip_thread,&attr,public_ip_worker,e))e->public_ip_started=1;
    pthread_attr_destroy(&attr);
    return rc;
}

void vr_control_stop(vr_engine *e)
{
    pthread_mutex_lock(&e->mutex);e->control_stop=1;pthread_mutex_unlock(&e->mutex);
    if(e->control_started)pthread_join(e->control_thread,NULL);
    if(e->public_ip_started)pthread_join(e->public_ip_thread,NULL);
}
