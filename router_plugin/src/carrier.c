#define _GNU_SOURCE
#include "engine.h"
#include <errno.h>
#include <poll.h>
#include <netinet/tcp.h>
#include <sys/ioctl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <unistd.h>
#include <openssl/crypto.h>
#include <openssl/sha.h>
#include <openssl/rand.h>
#include <openssl/err.h>

enum { TX_CONTROL,TX_BUSINESS,TX_BULK };
struct vr_tx_frame {
    vr_tx_frame *next;
    size_t length;
    uint64_t enqueued;
    unsigned lane;
    size_t frame_bytes;
    uint8_t bytes[];
};

/* Since Linux 3.12; vendor headers can predate the running router kernel. */
#ifndef TCP_NOTSENT_LOWAT
#define TCP_NOTSENT_LOWAT 25
#endif
#ifndef SIOCOUTQNSD
#define SIOCOUTQNSD 0x894b
#endif

static void carrier_socket(int fd)
{
    /* Do not shrink SO_SNDBUF: in-flight bytes still need a full BDP window.
       Unsupported kernels retain their existing behavior. */
    unsigned low=VR_TX_WRITE_MAX;
    (void)setsockopt(fd,IPPROTO_TCP,TCP_NOTSENT_LOWAT,&low,sizeof(low));
}

static int queue_class(int opcode,const void *bytes,size_t n)
{
    if(opcode==9||opcode==10)return TX_CONTROL;
    const uint8_t *p=bytes;
    if(n<12||p[0]!=0x56||p[1]!=0x52)return TX_BUSINESS;
    unsigned type=((unsigned)p[2]<<8)|p[3],sub=((unsigned)p[4]<<8)|p[5];
    if(type==2||type==3||type==5||type==6||(type==7&&sub==1))return TX_CONTROL;
    if(type==4||type==7)return TX_BULK;
    /* Keep main=8 lifecycle messages with data: close/result must not overtake
       data/open in this implementation's wire FIFO. */
    return TX_BUSINESS;
}

static vr_tx_frame *next_write(vr_link *l)
{
    if(l->writing)return l->writing;
    unsigned lane=TX_CONTROL;
    if(!l->tx_head[lane]) {
        if(l->tx_head[TX_BULK]&&l->business_since_bulk>=8)lane=TX_BULK;
        else lane=l->tx_head[TX_BUSINESS]?TX_BUSINESS:TX_BULK;
    }
    vr_tx_frame *f=l->tx_head[lane];if(!f)return NULL;
    l->tx_head[lane]=f->next;if(!l->tx_head[lane])l->tx_tail[lane]=NULL;
    f->next=NULL;l->writing=f;
    if(lane==TX_BUSINESS&&l->business_since_bulk<8)l->business_since_bulk++;
    if(lane==TX_BULK)l->business_since_bulk=0;
    return f;
}

static void write_done(vr_link *l)
{
    vr_tx_frame *f=l->writing;size_t memory=sizeof(*f)+f->length;
    l->tx_memory-=memory;l->tx_memory_class[f->lane]-=memory;
    l->tx_dequeued++;
    if(f->frame_bytes&&l->write_offset==f->length){l->frames_sent++;l->frame_bytes_sent+=f->frame_bytes;}
    free(f);l->writing=NULL;l->write_offset=0;
}

static int connected(vr_engine *e,const char *peer)
{for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl&&!strcmp(e->links[i].peer,peer))return 1;return 0;}

int vr_carrier_count(json_object *rule)
{return json_object_get_int(vr_member(rule,"carrier_count"))==1?1:VR_CARRIER_SLOTS;}

int vr_links_add(vr_engine *e,json_object *rule)
{
    size_t base;
    for(base=0;base<e->link_count;base+=VR_CARRIER_SLOTS)if(!e->links[base].rule)break;
    if(base+VR_CARRIER_SLOTS>VR_CARRIER_MAX)return -1;
    char route[33];if(vr_route_id(vr_string(rule,"id"),route))return -1;
    const char *from=vr_string(rule,"from_node_id"),*to=vr_string(rule,"to_node_id");
    for(size_t i=0;i<e->link_count;i++)if(!strcmp(e->links[i].route,route))return -1;
    for(int slot=0;slot<VR_CARRIER_SLOTS;slot++) {
        vr_link *l=&e->links[base+(size_t)slot];l->engine=e;l->rule=rule;l->fd=-1;l->slot=slot;
        strcpy(l->route,route);if(vr_copy(l->peer,sizeof(l->peer),!strcmp(from,e->config.node_id)?to:from))return -1;
    }
    if(base==e->link_count)e->link_count+=VR_CARRIER_SLOTS;
    return 0;
}

static json_object *local_rule(vr_engine *e,json_object *policy,const char *id)
{
    json_object *rules=vr_member(policy,"topology_rules");
    for(size_t i=0;i<(size_t)json_object_array_length(rules);i++) {
        json_object *r=json_object_array_get_idx(rules,i);
        if(!json_object_get_boolean(vr_member(r,"enabled"))||strcmp(vr_string(r,"id"),id))continue;
        if(!strcmp(vr_string(r,"from_node_id"),e->config.node_id)||!strcmp(vr_string(r,"to_node_id"),e->config.node_id))return r;
    }
    return NULL;
}

static const char *node_ip(json_object *policy,const char *id)
{
    json_object *nodes=vr_member(policy,"probe_ips");
    for(size_t i=0;i<(size_t)json_object_array_length(nodes);i++) {
        json_object *node=json_object_array_get_idx(nodes,i);
        if(!strcmp(vr_string(node,"node_id"),id))return vr_string(node,"ip");
    }
    return "";
}

static int same_connection(vr_engine *e,vr_link *l,json_object *r,json_object *policy)
{
    const char *fields[]={"from_node_id","to_node_id","secret","user_public_key","route_layer",
        "from_service_domain","to_service_domain","from_tls_spki_sha256","to_tls_spki_sha256"};
    if(!r)return 0;
    for(size_t i=0;i<sizeof(fields)/sizeof(*fields);i++)
        if(strcmp(vr_string(l->rule,fields[i]),vr_string(r,fields[i])))return 0;
    const char *peer=l->peer,*field=!strcmp(peer,vr_string(r,"from_node_id"))?"from_service_port":"to_service_port";
    return !strcmp(node_ip(e->policy,peer),node_ip(policy,peer))&&
        vr_policy_service_port(e->policy,peer,json_object_get_int(vr_member(l->rule,field)))==
        vr_policy_service_port(policy,peer,json_object_get_int(vr_member(r,field)));
}

void vr_links_reconcile(vr_engine *e,json_object *policy)
{
    /* Keep fixed array positions: flow affinity and queued TLS writes point at
       these slots. No handshake may be borrowing the rules at this boundary. */
    for(size_t base=0;base<e->link_count;base+=VR_CARRIER_SLOTS) {
        vr_link *first=&e->links[base];if(!first->rule)continue;
        json_object *r=local_rule(e,policy,vr_string(first->rule,"id"));
        int same=same_connection(e,first,r,policy);
        for(size_t i=base;i<base+VR_CARRIER_SLOTS;i++) {
            vr_link *l=&e->links[i];l->ingress_count=0;
            if(!same || l->slot>=vr_carrier_count(r)) {
                vr_link_close(l,r?"connection configuration changed":"topology link removed");
                l->retry=0;
            }
            if(!r){memset(l,0,sizeof(*l));l->fd=-1;continue;}
            l->rule=r;
            strcpy(l->peer,!strcmp(vr_string(r,"from_node_id"),e->config.node_id)?vr_string(r,"to_node_id"):vr_string(r,"from_node_id"));
        }
    }
    json_object *rules=vr_member(policy,"topology_rules");
    for(size_t i=0;i<(size_t)json_object_array_length(rules);i++) {
        json_object *r=json_object_array_get_idx(rules,i);
        if(local_rule(e,policy,vr_string(r,"id"))!=r)continue;
        size_t j;
        for(j=0;j<e->link_count;j++)if(e->links[j].rule==r)break;
        if(j==e->link_count)(void)vr_links_add(e,r); /* validated bounded topology */
    }
    while(e->link_count&&!e->links[e->link_count-VR_CARRIER_SLOTS].rule)e->link_count-=VR_CARRIER_SLOTS;
}

json_object *vr_links_status(vr_engine *e)
{
    json_object *rows=json_object_new_array();
    for(size_t i=0;i<e->link_count;i++) {
        vr_link *base=&e->links[i];if(!base->rule||base->slot)continue;
        json_object *row=json_object_new_object(),*slots=json_object_new_array(),*carriers=json_object_new_array();
        uint64_t tx=0,rx=0,rtt=0;int count=0;const char *error=base->error;
        for(size_t j=0;j<e->link_count;j++) {
            vr_link *l=&e->links[j];if(strcmp(l->route,base->route))continue;
            tx+=l->tx;rx+=l->rx;
            if(l->ssl){count++;json_object_array_add(slots,json_object_new_int(l->slot));if(!rtt)rtt=l->rtt;}
            json_object *v=json_object_new_object();
            json_object_object_add(v,"slot",json_object_new_int(l->slot));
            json_object_object_add(v,"state",json_object_new_string(l->ssl?"connected":"disconnected"));
            json_object_object_add(v,"tx_bytes",json_object_new_int64((int64_t)l->tx));
            json_object_object_add(v,"rx_bytes",json_object_new_int64((int64_t)l->rx));
            json_object_object_add(v,"queue_bytes",json_object_new_int64((int64_t)l->used));
            const char *names[]={"queue_control_bytes","queue_business_bytes","queue_bulk_bytes"};
            uint64_t oldest=l->writing?l->writing->enqueued:0,now=vr_millis();
            for(unsigned lane=0;lane<3;lane++) {
                json_object_object_add(v,names[lane],json_object_new_int64((int64_t)l->tx_bytes[lane]));
                if(l->tx_head[lane]&&(!oldest||l->tx_head[lane]->enqueued<oldest))oldest=l->tx_head[lane]->enqueued;
            }
            int pending=-1,unsent=-1;
            if(l->ssl&&l->fd>=0){(void)ioctl(l->fd,TIOCOUTQ,&pending);(void)ioctl(l->fd,SIOCOUTQNSD,&unsent);}
            json_object_object_add(v,"tcp_send_bytes",json_object_new_int(pending));
            json_object_object_add(v,"tcp_unsent_bytes",json_object_new_int(unsent));
            json_object_object_add(v,"queue_oldest_ms",json_object_new_int64((int64_t)(oldest?now-oldest:0)));
            json_object_object_add(v,"error",json_object_new_string(l->error));json_object_array_add(carriers,v);
        }
        json_object_object_add(row,"route",json_object_new_string(base->route));
        json_object_object_add(row,"peer",json_object_new_string(base->peer));
        json_object_object_add(row,"state",json_object_new_string(count?"connected":"disconnected"));
        const char *domain=vr_string(base->rule,!strcmp(base->peer,vr_string(base->rule,"from_node_id"))?"from_service_domain":"to_service_domain");
        json_object_object_add(row,"domain",json_object_new_string(domain));
        json_object_object_add(row,"transport",json_object_new_string("TLS/WebSocket"));
        json_object_object_add(row,"error",json_object_new_string(count?"":error));
        json_object_object_add(row,"rtt_ms",json_object_new_int64((int64_t)rtt));
        json_object_object_add(row,"tx_bytes",json_object_new_int64((int64_t)tx));
        json_object_object_add(row,"rx_bytes",json_object_new_int64((int64_t)rx));
        json_object_object_add(row,"carrier_count",json_object_new_int(count));
        json_object_object_add(row,"carrier_slots",slots);json_object_object_add(row,"carriers",carriers);
        json_object_array_add(rows,row);
    }
    return rows;
}

static uint32_t hash_byte(uint32_t hash,uint8_t b){return (hash^b)*16777619u;}
static uint32_t hash_bytes(uint32_t hash,const uint8_t *b,size_t n)
{for(size_t i=0;i<n;i++)hash=hash_byte(hash,b[i]);return hash;}

/* Identical to Probe's IPv4 endpoint-canonical FNV flow key. Control/speed
   frames always use the first healthy carrier, preserving their wire order. */
static uint32_t packet_hash(const uint8_t *b,size_t n)
{
    uint32_t h=2166136261u;if(n<20||b[0]>>4!=4)return h;
    size_t ihl=(b[0]&15)*4u,total=((size_t)b[2]<<8)|b[3];
    if(ihl<20||n<ihl||total<ihl||total>n)return h;
    uint8_t left[6]={0},right[6]={0};memcpy(left,b+12,4);memcpy(right,b+16,4);
    if((b[9]==6||b[9]==17)&&total>=ihl+4){memcpy(left+4,b+ihl,2);memcpy(right+4,b+ihl+2,2);}
    h=hash_byte(h,b[9]);int swap=memcmp(left,right,6)>0;
    h=hash_bytes(h,swap?right:left,6);h=hash_bytes(h,swap?left:right,6);
    if(b[9]==1&&total>=ihl+8)h=hash_bytes(h,b+ihl+4,2);
    return h;
}

static int flow_hash(const vr_frame *f,uint32_t *hash)
{
    if(f->main==1){*hash=packet_hash(f->data,f->data_len);return 1;}
    if(f->main!=8)return 0;
    char id[33];
    if(f->sub==3||f->sub==5||f->sub==6) {
        if(f->data_len<16)return 0;
        for(size_t i=0;i<16;i++)snprintf(id+2*i,3,"%02x",f->data[i]);
    } else {
        json_object *m=vr_json_parse(f->data,f->data_len);const char *s=vr_string(m,"session_id");
        if(strlen(s)!=32){json_object_put(m);return 0;}
        for(size_t i=0;i<32;i++) {
            char c=s[i];if(c>='A'&&c<='F')c=(char)(c+32);
            if(!((c>='0'&&c<='9')||(c>='a'&&c<='f'))){json_object_put(m);return 0;}
            id[i]=c;
        }
        id[32]=0;json_object_put(m);
    }
    *hash=hash_bytes(2166136261u,(const uint8_t *)id,32);return 1;
}

vr_link *vr_carrier_select(vr_engine *e,const char *peer,const vr_frame *f)
{
    vr_link *active[VR_CARRIER_SLOTS];size_t count=0;const char *route=NULL;
    for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl&&!strcmp(peer,e->links[i].peer)) {
        if(!route)route=e->links[i].route;
        if(!strcmp(route,e->links[i].route)&&count<VR_CARRIER_SLOTS)active[count++]=&e->links[i];
    }
    if(!count)return NULL;
    uint32_t hash;if(!flow_hash(f,&hash))return active[0];
    vr_flow_binding *oldest=&e->bindings[0];
    for(size_t i=0;i<VR_NAT_MAX;i++) {
        vr_flow_binding *b=&e->bindings[i];
        if(b->used&&b->hash==hash&&b->link>=0&&(size_t)b->link<e->link_count) {
            vr_link *l=&e->links[b->link];
            if(l->ssl&&l->session==b->session&&!strcmp(peer,l->peer)){b->used=vr_millis();return l;}
        }
        if(b->used<oldest->used)oldest=b;
    }
    vr_link *chosen=active[hash%count];oldest->hash=hash;oldest->link=(int)(chosen-e->links);
    oldest->session=chosen->session;oldest->used=vr_millis();return chosen;
}

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
    if(l->ssl&&*error){char event[240];snprintf(event,sizeof(event),"Peer %.32s slot %d disconnected: %.96s",l->peer,l->slot,error);vr_service_log(l->engine->service,"link",event);}
    SSL_free(l->ssl);l->ssl=NULL;if(l->fd>=0)close(l->fd);l->fd=-1;
    if(l->writing)write_done(l);
    for(unsigned lane=0;lane<3;lane++) {
        vr_tx_frame *f=l->tx_head[lane];while(f){vr_tx_frame *next=f->next;free(f);f=next;}
        l->tx_head[lane]=l->tx_tail[lane]=NULL;l->tx_bytes[lane]=l->tx_memory_class[lane]=0;
    }
    l->used=l->tx_memory=l->write_offset=l->write_pending=0;l->write_events=0;l->business_since_bulk=0;
    l->retry=vr_millis()+5000;snprintf(l->error,sizeof(l->error),"%s",error);
    if(l->decoder.peak>l->rx_peak)l->rx_peak=l->decoder.peak;
    memset(&l->decoder,0,sizeof(l->decoder));memset(&l->ws,0,sizeof(l->ws));
    l->ingress_count=0;
}

int vr_link_queue(vr_link *l,int opcode,const void *bytes,size_t n)
{
    if(!l->ssl||n>VR_TX_CAP-sizeof(vr_tx_frame)-14)return -1;
    unsigned lane=(unsigned)queue_class(opcode,bytes,n);
    size_t header=(n<126?2:n<=65535?4:10)+(l->client?4:0);
    size_t length=n+header,memory=sizeof(vr_tx_frame)+length;
    if(memory>VR_TX_CAP-l->tx_memory){l->tx_full++;return -1;}
    if(lane==TX_CONTROL) {
        if(memory>VR_TX_CONTROL_CAP-l->tx_memory_class[lane]){l->tx_full++;return -1;}
    } else {
        size_t business=l->tx_memory_class[TX_BUSINESS]+l->tx_memory_class[TX_BULK];
        if(memory>VR_TX_CAP-VR_TX_CONTROL_RESERVE-business){l->tx_full++;return -1;}
        if(lane==TX_BULK&&memory>VR_TX_BULK_CAP-l->tx_memory_class[lane]){l->tx_full++;return -1;}
    }
    vr_tx_frame *f=malloc(memory);if(!f)return -1;
    if(vr_ws_encode(opcode,l->client,bytes,n,f->bytes,length,&length)){free(f);return -1;}
    f->next=NULL;f->length=length;f->lane=lane;f->enqueued=vr_millis();
    f->frame_bytes=opcode==2?n:0;
    if(l->tx_tail[lane])l->tx_tail[lane]->next=f;else l->tx_head[lane]=f;
    l->tx_tail[lane]=f;l->used+=length;l->tx_bytes[lane]+=length;
    l->tx_memory+=memory;l->tx_memory_class[lane]+=memory;l->tx_enqueued++;
    if(l->used>l->tx_peak)l->tx_peak=l->used;
    if(l->tx_memory>l->tx_memory_peak)l->tx_memory_peak=l->tx_memory;
    return 0;
}

static int send_frame(vr_engine *e,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{
    size_t at;for(at=0;at<n;at++)if(!strcmp(path[at],e->config.node_id))break;
    if(at+1>=n)return -1;
    vr_link *l=vr_carrier_select(e,path[at+1],f);if(l) {
        uint8_t *b=malloc(12+f->control_len+f->data_len);size_t len;if(!b)return -1;
        int rc=vr_frame_encode(f,b,12+f->control_len+f->data_len,&len);
        if(!rc)rc=vr_link_queue(l,2,b,len);
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
    if(l->ingress_count && f->control_len==l->ingress_length &&
       (!f->control_len || !memcmp(f->control,l->ingress_control,f->control_len))) {
        count=l->ingress_count;at=l->ingress_at;memcpy(path,l->ingress_path,count*129);goto accepted;
    }
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
    if(f->control_len<=sizeof(l->ingress_control)) {
        if(f->control_len)memcpy(l->ingress_control,f->control,f->control_len);
        l->ingress_length=f->control_len;l->ingress_count=count;l->ingress_at=at;memcpy(l->ingress_path,path,count*129);
    }
accepted:
    l->frames_received++;l->frame_bytes_received+=12+f->control_len+f->data_len;
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
    /* WANT_READ from SSL_write must consume its own TLS handshake input first.
       Keep reading on WANT_WRITE: two saturated peers must still drain RX. */
    if(l->write_events!=POLLIN&&((events&POLLIN)||SSL_pending(l->ssl))) {
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
    if(l->used && (events&(l->write_events?l->write_events:POLLOUT))) {
        /* Bound work across peers; only preempt between whole WS frames. */
        size_t budget=4*VR_TX_WRITE_MAX;
        for(unsigned batch=0;batch<32&&budget&&l->used;batch++) {
            vr_tx_frame *f=next_write(l);if(!f)break;
            size_t n=l->write_pending;
            if(!n){n=f->length-l->write_offset;if(n>VR_TX_WRITE_MAX)n=VR_TX_WRITE_MAX;l->write_pending=n;}
            ERR_clear_error();errno=0;int sent=SSL_write(l->ssl,f->bytes+l->write_offset,(int)n);
            if(sent>0) {
                l->used-=(size_t)sent;l->tx_bytes[f->lane]-=(size_t)sent;
                l->write_offset+=(size_t)sent;l->write_pending=0;l->write_events=0;l->tx+=(unsigned)sent;
                budget=(size_t)sent<budget?budget-(size_t)sent:0;
                if(l->write_offset==f->length)write_done(l);
            } else {
                int error=SSL_get_error(l->ssl,sent);
                if(error==SSL_ERROR_WANT_READ)l->write_events=POLLIN;
                else if(error==SSL_ERROR_WANT_WRITE)l->write_events=POLLOUT;
                else {char detail[96];vr_tls_error(detail,sizeof(detail),"send",error);vr_link_close(l,detail);}
                break;
            }
        }
    }
}

static int header_equals(const char *header,const char *key,const char *value)
{char actual[1024];return vr_http_field(header,key,actual,sizeof(actual))&&!strcasecmp(actual,value);}

int vr_carrier_slot(const char *header)
{
    char raw[32];const char *value=vr_http_field(header,"X-Codex-VRouter-Carrier-Slot",raw,sizeof(raw));
    if(!value)return strcasestr(header,"\r\nX-Codex-VRouter-Carrier-Slot:")?-1:0;
    if(!*value)return 0;
    char *end;long parsed=strtol(value,&end,10);
    if(*end||parsed<0||parsed>=VR_CARRIER_SLOTS)return -1;
    return (int)parsed;
}

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
        !vr_http_field(header,"X-Codex-Auth-Mode",mode,sizeof(mode)) || strcmp(mode,"secret_hmac"))return -1;
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
    int slot=vr_carrier_slot(header);if(slot<0)goto fail;
    for(size_t i=0;i<e->link_count;i++)if(!strcmp(route,e->links[i].route)&&e->links[i].slot==slot){l=&e->links[i];break;}
    if(!l)goto fail;
    pthread_mutex_lock(&e->mutex);int auth=accept_auth(l,header,&expiry);pthread_mutex_unlock(&e->mutex);
    if(auth)goto fail;
    char reply[256];int n=snprintf(reply,sizeof(reply),"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nX-Codex-VRouter-Multi-Carrier: 1\r\nSec-WebSocket-Accept: %s\r\n\r\n",accept);
    if(vr_tls_io(ssl,1,reply,(size_t)n,deadline))goto fail;
    vr_link *result=calloc(1,sizeof(*result));if(!result)goto fail;
    result->engine=e;result->rule=l->rule;result->slot=slot;result->multi_carrier=1;strcpy(result->route,l->route);strcpy(result->peer,l->peer);
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
    int len=snprintf(request,16384,"GET %s HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nAuthorization: Bearer %s\r\nX-Codex-Route-Id: %s\r\nX-Codex-Api-Version: 2025-03-22\r\nX-Codex-Auth-Mode: secret_hmac\r\nX-Codex-Relay-Mode: bridge\r\nX-Codex-Relay-Role: to_next\r\nX-Codex-Source-Node-Id: %s\r\nX-Codex-User-Auth-Ticket: %s\r\nX-Codex-VRouter-Carrier-Slot: %d\r\nX-Codex-Mac: %s\r\n\r\n",VR_RELAY_PATH,host,port,ws_key,nonce,l->route,e->config.node_id,vr_string(r,"auth_ticket"),l->slot,mac);
    if(len<0||len>=16384 || vr_tls_io(ssl,1,request,(size_t)len,deadline) || vr_http_header(ssl,response,4096,deadline) || strncmp(response,"HTTP/1.1 101 ",13) ||
        !header_equals(response,"Upgrade","websocket") || !vr_http_token(response,"Connection","Upgrade") || !vr_http_field(response,"Sec-WebSocket-Accept",actual,sizeof(actual)) || strcmp(actual,accept))goto fail;
    l->multi_carrier=header_equals(response,"X-Codex-VRouter-Multi-Carrier","1");
    if(l->slot&&!l->multi_carrier)goto fail;
    l->ssl=ssl;l->fd=fd;l->client=1;l->ws.expect_mask=0;l->last_rx=vr_millis();l->auth_expires=expires;*l->error=0;free(request);free(response);
    snprintf(event,sizeof(event),"Peer %.32s slot %d %.120s:%d connected",l->peer,l->slot,host,port);vr_service_log(e->service,"link",event);return;
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
            strcpy(result->route,original->route);strcpy(result->peer,original->peer);result->slot=original->slot;result->fd=-1;dial_blocking(result);}
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
    if(!l->rule || l->ssl || vr_millis()<l->retry || strcmp(vr_string(l->rule,"from_node_id"),e->config.node_id))return;
    if(l->slot) {
        if(l->slot>=vr_carrier_count(l->rule))return;
        int supported=0;for(size_t i=0;i<e->link_count;i++) {
            vr_link *primary=&e->links[i];
            if(!primary->slot&&!strcmp(primary->route,l->route)&&primary->multi_carrier)supported=1;
        }
        if(!supported)return;
    }
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
            vr_link *l=&e->links[j];if(strcmp(l->route,result->route)||l->slot!=result->slot)continue;
            if(result->ssl) {
                vr_link_close(l,"");l->ssl=result->ssl;l->fd=result->fd;l->client=result->client;
                carrier_socket(l->fd);
                l->ws.expect_mask=result->ws.expect_mask;l->last_rx=result->last_rx;l->auth_expires=result->auth_expires;
                l->multi_carrier=result->multi_carrier;l->keepalive_at=vr_millis();l->connected_at=l->keepalive_at;l->session=++e->carrier_session;l->opens++;*l->error=0;
                if(!l->slot&&!l->multi_carrier)for(size_t k=0;k<e->link_count;k++)
                    if(e->links[k].slot&&!strcmp(e->links[k].route,l->route))vr_link_close(&e->links[k],"");
                result->ssl=NULL;result->fd=-1;
            } else if(!l->ssl){snprintf(l->error,sizeof(l->error),"%s",result->error);l->retry=vr_millis()+10000;}
            break;
        }
        vr_link_close(result,"");free(result);job->result=NULL;
    }
}
