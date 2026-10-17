#define _GNU_SOURCE
#include "engine.h"
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <unistd.h>
#include <openssl/crypto.h>
#include <openssl/sha.h>
#include <openssl/rand.h>
#include <openssl/err.h>

static int connected(vr_engine *e,const char *peer)
{for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl&&!strcmp(e->links[i].peer,peer))return 1;return 0;}

static int adjacent(vr_engine *e,const char *a,const char *b)
{
    json_object *links=vr_member(e->policy,"topology_rules");
    for(size_t i=0;i<(size_t)json_object_array_length(links);i++) {
        json_object *r=json_object_array_get_idx(links,i);if(!json_object_get_boolean(vr_member(r,"enabled")))continue;
        const char *from=vr_string(r,"from_node_id"),*to=vr_string(r,"to_node_id");
        if((!strcmp(from,a)&&!strcmp(to,b))||(!strcmp(from,b)&&!strcmp(to,a)))return 1;
    }
    return 0;
}

int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n)
{
    memset(path,0,VR_PATH_MAX*129);
    json_object *nodes=vr_member(e->policy,"probe_ips");size_t count=json_object_array_length(nodes);
    int parent[256],queue[256],start=-1,goal=-1;size_t head=0,tail=0;
    for(size_t i=0;i<count;i++) {parent[i]=-2;const char *id=vr_string(json_object_array_get_idx(nodes,i),"node_id");if(!strcmp(id,e->config.node_id))start=(int)i;if(!strcmp(id,target))goal=(int)i;}
    if(start<0||goal<0||start==goal)return -1;
    queue[tail++]=start;parent[start]=-1;
    while(head<tail && parent[goal]==-2) {
        int at=queue[head++];const char *from=vr_string(json_object_array_get_idx(nodes,(size_t)at),"node_id");
        for(size_t i=0;i<count;i++) {
            const char *to=vr_string(json_object_array_get_idx(nodes,i),"node_id");
            if(parent[i]!=-2 || (at==start&&!connected(e,to)) || !adjacent(e,from,to))continue;
            parent[i]=at;queue[tail++]=(int)i;
        }
    }
    if(parent[goal]==-2)return -1;
    int reverse[VR_PATH_MAX];size_t used=0;
    for(int at=goal;at>=0;at=parent[at]) {if(used>=VR_PATH_MAX)return -1;reverse[used++]=at;}
    for(size_t i=0;i<used;i++)if(vr_copy(path[i],129,vr_string(json_object_array_get_idx(nodes,(size_t)reverse[used-i-1]),"node_id")))return -1;
    if(used>VR_PATH_HOPS+1)return -1;
    *n=used;return 0;
}

void vr_link_close(vr_link *l,const char *error)
{
    if(l->ssl&&*error){char event[240];snprintf(event,sizeof(event),"Peer %.32s disconnected: %.96s",l->peer,error);vr_service_log(l->engine->service,"link",event);}
    SSL_free(l->ssl);l->ssl=NULL;if(l->fd>=0)close(l->fd);l->fd=-1;l->used=0;l->head=0;l->write_pending=0;
    l->retry=vr_millis()+5000;snprintf(l->error,sizeof(l->error),"%s",error);
    memset(&l->decoder,0,sizeof(l->decoder));memset(&l->ws,0,sizeof(l->ws));
}

int vr_link_queue(vr_link *l,int opcode,const void *bytes,size_t n)
{
    if(!l->ssl || n+14>VR_TX_CAP-l->used)return -1;
    uint8_t *encoded=malloc(n+14);if(!encoded)return -1;size_t length;
    if(vr_ws_encode(opcode,l->client,bytes,n,encoded,n+14,&length)){free(encoded);return -1;}
    size_t at=(l->head+l->used)%VR_TX_CAP,first=VR_TX_CAP-at;if(first>length)first=length;
    memcpy(l->queue+at,encoded,first);memcpy(l->queue,encoded+first,length-first);l->used+=length;free(encoded);return 0;
}

static int send_frame(vr_engine *e,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{
    size_t at;for(at=0;at<n;at++)if(!strcmp(path[at],e->config.node_id))break;
    if(at+1>=n)return -1;
    for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl && !strcmp(path[at+1],e->links[i].peer)) {
        uint8_t *b=malloc(12+f->control_len+f->data_len);size_t len;if(!b)return -1;
        int rc=vr_frame_encode(f,b,12+f->control_len+f->data_len,&len);
        if(!rc)rc=vr_link_queue(&e->links[i],2,b,len);
        free(b);return rc;
    }
    return -1;
}

int vr_send(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,const void *data,size_t length)
{
    json_object *o=json_object_new_object(),*a=json_object_new_array();
    for(size_t i=0;i<n;i++)json_object_array_add(a,json_object_new_string(path[i]));
    json_object_object_add(o,"path",a);
    const char *control=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);
    vr_frame f={main,sub,(const uint8_t *)control,data,strlen(control),length};int rc=send_frame(e,&f,path,n);json_object_put(o);return rc;
}

int vr_send_json(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,json_object *m)
{const char *s=json_object_to_json_string_ext(m,JSON_C_TO_STRING_PLAIN);return vr_send(e,main,sub,path,n,s,strlen(s));}

int vr_link_receive(const vr_frame *f,void *arg)
{
    vr_link *l=arg;vr_engine *e=l->engine;char path[VR_PATH_MAX][129];size_t count,at;
    const char *rejected=NULL;
    /* Probe Node permits an omitted/empty path and uses this carrier's ingress
       path. Malformed JSON and explicit invalid paths still fail closed. */
    json_object *control=f->control_len?vr_json_parse(f->control,f->control_len):NULL;
    json_object *a=vr_member(control,"path");
    int fallback=(!f->control_len || (control && json_object_is_type(control,json_type_object) &&
        (!a || json_object_is_type(a,json_type_null) || (json_object_is_type(a,json_type_array)&&!json_object_array_length(a)))));
    json_object_put(control);
    if(fallback){memset(path,0,sizeof(path));strcpy(path[0],l->peer);strcpy(path[1],e->config.node_id);count=2;}
    else if(vr_frame_path(f,path,&count)){rejected="invalid business path";goto drop;}
    if(count>VR_PATH_HOPS+1){rejected="path exceeds maximum hops";goto drop;}
    for(at=0;at<count;at++)if(!strcmp(path[at],e->config.node_id))break;
    if(!at || at>=count || strcmp(path[at-1],l->peer)){rejected="ingress peer mismatch";goto drop;}
    for(size_t i=1;i<count;i++)if(!adjacent(e,path[i-1],path[i])){rejected="unauthorized path edge";goto drop;}
    if(at+1<count){send_frame(e,f,path,count);return 0;}
    vr_link_frame(l,f,path,count);return 0;
drop:;
    /* Match Probe Node: reject the business frame, not its healthy carrier. */
    char event[240];snprintf(event,sizeof(event),"Peer %.32s dropped frame %u/%u: %s",l->peer,f->main,f->sub,rejected);
    vr_service_log(e->service,"link",event);return 0;
}

static int ws_received(int opcode,const uint8_t *bytes,size_t n,void *arg)
{
    vr_link *l=arg;
    if(opcode==2){int rc=vr_decoder_feed(&l->decoder,bytes,n,vr_link_receive,l);
        if(rc)snprintf(l->error,sizeof(l->error),"VR envelope invalid (code %d, buffered %zu/%zu)",rc,l->decoder.used,l->decoder.needed);
        return rc;}
    if(opcode==9)return vr_link_queue(l,10,bytes,n);
    if(opcode==8){unsigned code=n>=2?((unsigned)bytes[0]<<8)|bytes[1]:1005;
        snprintf(l->error,sizeof(l->error),"peer WebSocket close (%u)",code);return -1;}
    return 0;
}

void vr_link_poll(vr_link *l,short events)
{
    if(events&(POLLERR|POLLHUP|POLLNVAL)){vr_link_close(l,"connection closed");return;}
    if((events&POLLIN)||SSL_pending(l->ssl)) {
        /* Drain a bounded batch. Reading one record per 50 ms poll can leave
           decrypted records stranded in OpenSSL and stall a remote download. */
        for(unsigned batch=0;batch<16;batch++) {
            ERR_clear_error();errno=0;
            uint8_t bytes[16384];int n=SSL_read(l->ssl,bytes,sizeof(bytes));
            if(n>0){l->rx+=(unsigned)n;l->last_rx=vr_millis();*l->error=0;
                if(vr_ws_feed(&l->ws,bytes,(size_t)n,ws_received,l)){
                    char detail[sizeof(l->error)];
                    if(*l->error)snprintf(detail,sizeof(detail),"%s",l->error);
                    else snprintf(detail,sizeof(detail),"WebSocket invalid (opcode %d, mask %d, header %02x/%02x)",l->ws.opcode,l->ws.expect_mask,l->ws.header[0],l->ws.header[1]);
                    vr_link_close(l,detail);return;}}
            else {int error=SSL_get_error(l->ssl,n);if(error!=SSL_ERROR_WANT_READ&&error!=SSL_ERROR_WANT_WRITE){char detail[96];vr_tls_error(detail,sizeof(detail),"receive",error);vr_link_close(l,detail);return;}break;}
        }
    }
    if(l->used && (events&POLLOUT)) {
        size_t n=l->write_pending;
        if(!n){n=VR_TX_CAP-l->head;if(n>l->used)n=l->used;l->write_pending=n;}
        ERR_clear_error();errno=0;int sent=SSL_write(l->ssl,l->queue+l->head,(int)n);
        if(sent>0){l->head=(l->head+(size_t)sent)%VR_TX_CAP;l->used-=(size_t)sent;l->write_pending=0;l->tx+=(unsigned)sent;}
        else {int error=SSL_get_error(l->ssl,sent);if(error!=SSL_ERROR_WANT_READ&&error!=SSL_ERROR_WANT_WRITE){char detail[96];vr_tls_error(detail,sizeof(detail),"send",error);vr_link_close(l,detail);}}
    }
}

static int header_equals(const char *header,const char *key,const char *value)
{char actual[1024];return vr_http_field(header,key,actual,sizeof(actual))&&!strcasecmp(actual,value);}

static int relay_request_line(const char *header,const char *route)
{
    const char *plain="GET " VR_RELAY_PATH " HTTP/1.1\r\n";
    if(!strncmp(header,plain,strlen(plain)))return 1;
    char expected[256];int n=snprintf(expected,sizeof(expected),"GET %s?route_id=%s HTTP/1.1\r\n",VR_RELAY_PATH,route);
    return n>0 && n<(int)sizeof(expected) && !strncmp(header,expected,(size_t)n);
}

static int accept_auth(vr_link *l,const char *header,time_t *expiry)
{
    vr_engine *e=l->engine;char auth[300],nonce_mac[128],source[129],role[32],ticket[8193],mode[32];
    if(!vr_http_field(header,"Authorization",auth,sizeof(auth)) || strncmp(auth,"Bearer ",7) || strlen(auth+7)<16 || strlen(auth+7)>256 ||
        !vr_http_field(header,"X-Codex-Mac",nonce_mac,sizeof(nonce_mac)) ||
        !vr_http_field(header,"X-Codex-Source-Node-Id",source,sizeof(source)) || strcmp(source,l->peer) ||
        !vr_http_field(header,"X-Codex-Relay-Role",role,sizeof(role)) ||
        strcmp(role,!strcmp(l->peer,vr_string(l->rule,"from_node_id"))?"to_next":"to_prev") ||
        !vr_http_field(header,"X-Codex-User-Auth-Ticket",ticket,sizeof(ticket)) ||
        !vr_http_field(header,"X-Codex-Auth-Mode",mode,sizeof(mode)) || strcmp(mode,"secret_hmac") ||
        !header_equals(header,"X-Codex-VRouter-Carrier-Slot","0"))return -1;
    const char *secret=vr_string(l->rule,"secret");if(!*secret || strlen(secret)>1024)return -1;
    char text[2048],expected[65];snprintf(text,sizeof(text),"%s\n%s\nGET\n%s\n%s\n%s",l->route,auth+7,VR_RELAY_PATH,source,role);
    if(vr_hmac(secret,text,expected) || strlen(nonce_mac)!=64 || CRYPTO_memcmp(expected,nonce_mac,64) ||
        vr_ticket_verify(ticket,vr_string(l->rule,"user_public_key"),l->route,vr_string(l->rule,"from_node_id"),vr_string(l->rule,"to_node_id"),time(NULL),expiry))return -1;
    unsigned char digest[32];char key[65];SHA256((unsigned char *)text,strlen(text),digest);
    for(unsigned i=0;i<32;i++)snprintf(key+2*i,3,"%02x",digest[i]);
    /* Keep a bounded, single security-state file, not a connection log. */
    json_object *fresh=json_object_new_object();time_t now=time(NULL);int count=0,replayed=0;
    json_object_object_foreach(e->nonces,k,v) {
        if(json_object_get_int64(v)<=now)continue;
        if(!strcmp(k,key))replayed=1;
        json_object_object_add(fresh,k,json_object_new_int64(json_object_get_int64(v)));count++;
    }
    if(replayed || count>=1024){json_object_put(fresh);return -1;}
    json_object_object_add(fresh,key,json_object_new_int64(*expiry));const char *saved=json_object_to_json_string_ext(fresh,JSON_C_TO_STRING_PLAIN);
    if(vr_atomic_write(e->replay_path,saved,strlen(saved))){json_object_put(fresh);return -1;}
    json_object_put(e->nonces);e->nonces=fresh;return 0;
}

static vr_link *accept_blocking(vr_engine *e,int fd)
{
    uint64_t deadline=vr_millis()+4000;SSL *ssl=vr_tls_accept(e->tls,fd,deadline);if(!ssl){close(fd);return NULL;}
    char *header=malloc(16384);char route[64],key[64],accept[29];vr_link *l=NULL;time_t expiry;
    if(!header || vr_http_header(ssl,header,16384,deadline) ||
        !header_equals(header,"Upgrade","websocket") || !vr_http_token(header,"Connection","Upgrade") || !header_equals(header,"Sec-WebSocket-Version","13") ||
        !vr_http_field(header,"X-Codex-Route-Id",route,sizeof(route)) || !relay_request_line(header,route) || !vr_http_field(header,"Sec-WebSocket-Key",key,sizeof(key)) || vr_ws_accept_key(key,accept))goto fail;
    for(size_t i=0;i<e->link_count;i++)if(!strcmp(route,e->links[i].route)){l=&e->links[i];break;}
    if(!l)goto fail;
    pthread_mutex_lock(&e->mutex);int auth=accept_auth(l,header,&expiry);pthread_mutex_unlock(&e->mutex);
    if(auth)goto fail;
    char reply[256];int n=snprintf(reply,sizeof(reply),"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",accept);
    if(vr_tls_io(ssl,1,reply,(size_t)n,deadline))goto fail;
    vr_link *result=calloc(1,sizeof(*result));if(!result)goto fail;
    result->engine=e;result->rule=l->rule;strcpy(result->route,l->route);strcpy(result->peer,l->peer);
    result->ssl=ssl;result->fd=fd;result->client=0;result->ws.expect_mask=1;result->last_rx=vr_millis();result->auth_expires=expiry;free(header);return result;
fail:free(header);SSL_free(ssl);close(fd);return NULL;
}

static void dial_blocking(vr_link *l)
{
    vr_engine *e=l->engine;json_object *r=l->rule;
    if(l->ssl || strcmp(vr_string(r,"from_node_id"),e->config.node_id) || vr_millis()<l->retry)return;
    l->retry=vr_millis()+10000;
    const char *host=vr_string(r,"to_service_domain"),*pin=vr_string(r,"to_tls_spki_sha256");int port=json_object_get_int(vr_member(r,"to_service_port"));
    port=vr_policy_service_port(e->policy,vr_string(r,"to_node_id"),port);
    time_t expires;
    if(vr_ticket_verify(vr_string(r,"auth_ticket"),vr_string(r,"user_public_key"),l->route,vr_string(r,"from_node_id"),vr_string(r,"to_node_id"),time(NULL),&expires)){strcpy(l->error,"route ticket invalid or expired");return;}
    uint64_t deadline=vr_millis()+5000;int fd=-1;char event[240];
    SSL *ssl=vr_tls_connect(host,port,pin,&e->config,e->addresses.wan_if,&fd,deadline);if(!ssl){strcpy(l->error,"DNS/TCP/TLS connection failed");
        snprintf(event,sizeof(event),"Peer %.32s %.120s:%d: %.64s",l->peer,host,port,l->error);vr_service_log(e->service,"link",event);return;}
    char nonce[49],text[1024],mac[65],ws_key[25],accept[29],actual[64];unsigned char random[16];
    char *request=malloc(16384),*response=malloc(4096);
    if(!request || !response || vr_random_hex(nonce,24) || RAND_bytes(random,16)!=1)goto fail;
    EVP_EncodeBlock((unsigned char *)ws_key,random,16);if(vr_ws_accept_key(ws_key,accept))goto fail;
    snprintf(text,sizeof(text),"%s\n%s\nGET\n%s\n%s\nto_next",l->route,nonce,VR_RELAY_PATH,e->config.node_id);
    if(vr_hmac(vr_string(r,"secret"),text,mac))goto fail;
    int len=snprintf(request,16384,"GET %s HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nAuthorization: Bearer %s\r\nX-Codex-Route-Id: %s\r\nX-Codex-Api-Version: 2025-03-22\r\nX-Codex-Auth-Mode: secret_hmac\r\nX-Codex-Relay-Mode: bridge\r\nX-Codex-Relay-Role: to_next\r\nX-Codex-Source-Node-Id: %s\r\nX-Codex-User-Auth-Ticket: %s\r\nX-Codex-VRouter-Carrier-Slot: 0\r\nX-Codex-Mac: %s\r\n\r\n",VR_RELAY_PATH,host,port,ws_key,nonce,l->route,e->config.node_id,vr_string(r,"auth_ticket"),mac);
    if(len<0||len>=16384 || vr_tls_io(ssl,1,request,(size_t)len,deadline) || vr_http_header(ssl,response,4096,deadline) || strncmp(response,"HTTP/1.1 101 ",13) ||
        !header_equals(response,"Upgrade","websocket") || !vr_http_token(response,"Connection","Upgrade") || !vr_http_field(response,"Sec-WebSocket-Accept",actual,sizeof(actual)) || strcmp(actual,accept))goto fail;
    l->ssl=ssl;l->fd=fd;l->client=1;l->ws.expect_mask=0;l->last_rx=vr_millis();l->auth_expires=expires;*l->error=0;free(request);free(response);
    snprintf(event,sizeof(event),"Peer %.32s %.120s:%d connected",l->peer,host,port);vr_service_log(e->service,"link",event);return;
fail:free(request);free(response);SSL_free(ssl);close(fd);strcpy(l->error,"carrier handshake failed");
    snprintf(event,sizeof(event),"Peer %.32s %.120s:%d: carrier handshake failed",l->peer,host,port);vr_service_log(e->service,"link",event);
}

static void *handshake_worker(void *arg)
{
    vr_handshake *job=arg;vr_engine *e=job->engine;vr_link *result=NULL;
    if(job->index<0)result=accept_blocking(e,job->fd);
    else {
        result=calloc(1,sizeof(*result));
        if(result){vr_link *original=&e->links[job->index];result->engine=e;result->rule=original->rule;
            strcpy(result->route,original->route);strcpy(result->peer,original->peer);result->fd=-1;dial_blocking(result);}
    }
    pthread_mutex_lock(&e->mutex);job->result=result;job->done=1;pthread_mutex_unlock(&e->mutex);return NULL;
}

static int start_handshake(vr_engine *e,int fd,int index)
{
    for(size_t i=0;i<2;i++) {
        vr_handshake *job=&e->handshakes[i];if(job->started)continue;
        job->engine=e;job->fd=fd;job->index=index;job->done=0;job->started=1;job->result=NULL;
        pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,256*1024);
        int rc=pthread_create(&job->thread,&attr,handshake_worker,job);pthread_attr_destroy(&attr);
        if(rc){job->started=0;return -1;}return 0;
    }
    return -1;
}

void vr_link_dial(vr_link *l)
{
    vr_engine *e=l->engine;
    if(l->ssl || vr_millis()<l->retry || strcmp(vr_string(l->rule,"from_node_id"),e->config.node_id))return;
    int index=(int)(l-e->links);
    for(size_t i=0;i<2;i++)if(e->handshakes[i].started&&e->handshakes[i].index==index)return;
    if(!start_handshake(e,-1,index))l->retry=vr_millis()+10000;
}

void vr_link_accept(vr_engine *e,int fd)
{if(start_handshake(e,fd,-1))close(fd);}

void vr_handshakes_poll(vr_engine *e,int finish)
{
    for(size_t i=0;i<2;i++) {
        vr_handshake *job=&e->handshakes[i];if(!job->started)continue;
        pthread_mutex_lock(&e->mutex);int done=job->done;pthread_mutex_unlock(&e->mutex);
        if(!done&&!finish)continue;
        pthread_join(job->thread,NULL);job->started=0;vr_link *result=job->result;
        if(!result)continue;
        if(!finish)for(size_t j=0;j<e->link_count;j++) {
            vr_link *l=&e->links[j];if(strcmp(l->route,result->route))continue;
            if(result->ssl) {
                vr_link_close(l,"");l->ssl=result->ssl;l->fd=result->fd;l->client=result->client;
                l->ws.expect_mask=result->ws.expect_mask;l->last_rx=result->last_rx;l->auth_expires=result->auth_expires;
                result->ssl=NULL;result->fd=-1;
            } else if(!l->ssl){snprintf(l->error,sizeof(l->error),"%s",result->error);l->retry=vr_millis()+10000;}
            break;
        }
        vr_link_close(result,"");free(result);job->result=NULL;
    }
}
