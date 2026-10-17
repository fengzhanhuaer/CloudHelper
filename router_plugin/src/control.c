#define _GNU_SOURCE
#include "engine.h"
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/sysinfo.h>
#include <unistd.h>
#include <openssl/rand.h>
#include <openssl/err.h>

/* The existing controller carries one JSON control stream over yamux/WS.
 * Implement only that bounded stream; no general mux runtime or new library. */
typedef struct {
    vr_engine *engine;SSL *ssl;int fd,refresh,report;
    vr_ws_decoder ws;
    uint8_t header[12];size_t header_used,payload_left;
    uint32_t send_window;size_t message_used;
    char message[16384];uint64_t ack_at;
} control;

static uint32_t u32(const uint8_t *b){return ((uint32_t)b[0]<<24)|((uint32_t)b[1]<<16)|((uint32_t)b[2]<<8)|b[3];}
static void p32(uint8_t *b,uint32_t n){b[0]=(uint8_t)(n>>24);b[1]=(uint8_t)(n>>16);b[2]=(uint8_t)(n>>8);b[3]=(uint8_t)n;}

static int stopped(vr_engine *e)
{
    pthread_mutex_lock(&e->mutex);int stop=e->control_stop;pthread_mutex_unlock(&e->mutex);
    return stop||vr_service_stopped(e->service,e->generation);
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
    if(!strcmp(type,"ack")){c->ack_at=vr_millis();pthread_mutex_lock(&c->engine->mutex);c->engine->controller_connected=1;pthread_mutex_unlock(&c->engine->mutex);}
    else if(!strcmp(type,"route_config_sync"))c->refresh=1;
    else if(!strcmp(type,"report_once"))c->report=1;
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
    vr_engine *e=c->engine;char challenge[513],signature[65],host[254],key[25],accept[29],actual[64];int port,tls;
    char error[128]="invalid controller URL";
    if(vr_origin(e->config.controller,host,&port,&tls)||!tls||
       vr_controller_auth(&e->config,e->addresses.wan_if,"GET","/api/probe",challenge,signature,error,sizeof(error))||stopped(e)){
        if(!stopped(e))vr_service_log(e->service,"controller",error);
        return -1;
    }
    uint64_t deadline=vr_millis()+5000;
    c->ssl=vr_tls_connect(host,port,"",&e->config,e->addresses.wan_if,&c->fd,deadline);if(!c->ssl){vr_service_log(e->service,"controller","Control stream DNS/TCP/TLS connection failed");return -1;}
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
    vr_engine *e=c->engine;json_object *o=vr_probe_report(&e->config,&e->addresses,"/sys/class/net");
    const char *text=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);size_t len=strlen(text);int rc=-1;
    char *line=malloc(len+1);
    if(line&&len<16384){memcpy(line,text,len);line[len++]='\n';rc=send_mux(c,0,0,1,(uint32_t)len,line);}
    free(line);
    json_object_put(o);return rc;
}

static int refresh(control *c,const char *original)
{
    vr_engine *e=c->engine;char error[256];
    json_object *next=vr_controller_call(&e->config,e->addresses.wan_if,"GET","/api/probe/route/config",NULL,error,sizeof(error));
    if(!next){vr_service_log(e->service,"controller","Configuration refresh failed; retaining current policy and control stream");return -1;}
    if(vr_policy_validate(next,e->config.node_id,error,sizeof(error))){vr_service_log(e->service,"controller",error);json_object_put(next);return -1;}
    int changed=strcmp(original,json_object_to_json_string_ext(next,JSON_C_TO_STRING_PLAIN))!=0;
    if(changed) {
        vr_service *s=e->service;pthread_mutex_lock(&s->mutex);
        if(s->stop||s->generation!=e->generation){pthread_mutex_unlock(&s->mutex);json_object_put(next);return -1;}
        json_object_put(s->pending_policy);s->pending_policy=json_object_get(next);s->pending_generation=e->generation;
        pthread_mutex_unlock(&s->mutex);
        pthread_mutex_lock(&e->mutex);e->refresh=1;pthread_mutex_unlock(&e->mutex);
        vr_service_log(s,"controller","Validated configuration received; applying without another download");
    }
    json_object_put(next);return changed?1:0;
}

static void *control_worker(void *arg)
{
    vr_engine *e=arg;char *original=strdup(json_object_to_json_string_ext(e->snapshot,JSON_C_TO_STRING_PLAIN));if(!original)return NULL;
    while(!stopped(e)) {
        control *c=calloc(1,sizeof(*c));if(!c)break;c->engine=e;c->fd=-1;
        if(!connect_controller(c)) {
            vr_service_log(e->service,"controller","Control stream established");uint64_t sent=0,next_check=vr_millis()+60000;unsigned failures=0;
            while(!stopped(e)) {
                uint64_t now=vr_millis();
                if(now-c->ack_at>65000)break;
                if(c->report||now-sent>=20000){if(report(c))break;sent=now;c->report=0;}
                if(c->refresh||now>=next_check){int rc=refresh(c,original);c->refresh=0;if(rc>0)break;
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
        SSL_free(c->ssl);if(c->fd>=0)close(c->fd);free(c);
        pthread_mutex_lock(&e->mutex);e->controller_connected=0;int changed=e->refresh;pthread_mutex_unlock(&e->mutex);if(changed)break;
        for(int i=0;i<50&&!stopped(e);i++)poll(NULL,0,100);
    }
    free(original);return NULL;
}

int vr_control_start(vr_engine *e)
{
    pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,256*1024);
    int rc=pthread_create(&e->control_thread,&attr,control_worker,e);pthread_attr_destroy(&attr);
    if(!rc)e->control_started=1;
    return rc;
}

void vr_control_stop(vr_engine *e)
{
    pthread_mutex_lock(&e->mutex);e->control_stop=1;pthread_mutex_unlock(&e->mutex);
    if(e->control_started)pthread_join(e->control_thread,NULL);
}
