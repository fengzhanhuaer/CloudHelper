#define _GNU_SOURCE
#include "engine.h"
#include <errno.h>
#include <arpa/inet.h>
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
enum { TX_SMALL_PACKET = 128, TX_BATCH_BYTES = 1024, TX_BATCH_FRAMES = 8 };
struct vr_tx_frame {
    vr_tx_frame *next;
    size_t length;
    size_t capacity;
    uint64_t enqueued;
    unsigned lane;
    unsigned count;
    size_t frame_bytes;
    uint8_t bytes[];
};

uint64_t vr_link_oldest_wait_ms(vr_link *l,uint64_t now)
{
    uint64_t oldest=l->writing?l->writing->enqueued:0;
    for(unsigned lane=0;lane<3;lane++)if(l->tx_head[lane]&&(!oldest||l->tx_head[lane]->enqueued<oldest))oldest=l->tx_head[lane]->enqueued;
    if(oldest&&oldest<l->tx_stats_reset_at)oldest=l->tx_stats_reset_at;
    return oldest&&now>=oldest?now-oldest:0;
}

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

static int queue_class(const void *bytes,size_t n)
{
    const uint8_t *p=bytes;
    if(n<12||p[0]!=0x56||p[1]!=0x52)return TX_BUSINESS;
    unsigned type=((unsigned)p[2]<<8)|p[3],sub=((unsigned)p[4]<<8)|p[5];
    if(type==VR_KEEPALIVE_MAIN||type==2||type==3||type==5||type==6||(type==7&&sub==1))return TX_CONTROL;
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
    uint64_t now=vr_millis(),since=f->enqueued>l->tx_stats_reset_at?f->enqueued:l->tx_stats_reset_at,wait=now>=since?now-since:0;
    l->tx_wait_samples+=f->count;l->tx_wait_total_ms+=wait*f->count;if(wait>l->tx_wait_max_ms)l->tx_wait_max_ms=wait;
    f->next=NULL;l->writing=f;
    if(lane==TX_BUSINESS&&l->business_since_bulk<8) {
        l->business_since_bulk+=f->count;
        if(l->business_since_bulk>8)l->business_since_bulk=8;
    }
    if(lane==TX_BULK)l->business_since_bulk=0;
    return f;
}

static void write_done(vr_link *l)
{
    vr_tx_frame *f=l->writing;size_t memory=sizeof(*f)+f->capacity;
    l->tx_memory-=memory;l->tx_memory_class[f->lane]-=memory;
    l->tx_dequeued+=f->count;
    if(f->frame_bytes&&l->write_offset==f->length){l->frames_sent+=f->count;l->frame_bytes_sent+=f->frame_bytes;}
    free(f);l->writing=NULL;l->write_offset=0;
    if(!l->tx_memory&&l->tx_backlog_since){uint64_t span=vr_millis()-l->tx_backlog_since;if(span>l->tx_backlog_peak_ms)l->tx_backlog_peak_ms=span;l->tx_backlog_since=0;}
}

static int connected(vr_engine *e,const char *peer)
{for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl&&!strcmp(e->links[i].peer,peer))return 1;return 0;}

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
    const char *fields[]={"from_node_id","to_node_id","secret","user_id","user_public_key","route_layer",
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
    memset(e->packet_cache,0,sizeof(e->packet_cache));
    /* Keep fixed array positions: flow affinity and queued TLS writes point at
       these slots. No handshake may be borrowing the rules at this boundary. */
    for(size_t base=0;base<e->link_count;base+=VR_CARRIER_SLOTS) {
        vr_link *first=&e->links[base];if(!first->rule)continue;
        json_object *r=local_rule(e,policy,vr_string(first->rule,"id"));
        int same=same_connection(e,first,r,policy);
        for(size_t i=base;i<base+VR_CARRIER_SLOTS;i++) {
            vr_link *l=&e->links[i];l->ingress_count=0;
            if(!same) {
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
        json_object_object_add(row,"transport",json_object_new_string(VR_DIRECT_TRANSPORT));
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

static int packet_identity(const uint8_t *p,size_t n,uint8_t key[13])
{
    if(n<20||p[0]>>4!=4||(p[9]!=6&&p[9]!=17))return 0;
    size_t ihl=(p[0]&15u)*4u,total=((size_t)p[2]<<8)|p[3];
    if(ihl<20||total!=n||n<ihl+(p[9]==6?20u:8u))return 0;
    key[0]=p[9];memcpy(key+1,p+12,8);memcpy(key+9,p+ihl,4);return 1;
}

static vr_packet_cache *packet_cache_find(vr_engine *e,const uint8_t *p,size_t n)
{
    uint8_t key[13];if(!packet_identity(p,n,key))return NULL;
    vr_packet_cache *c=&e->packet_cache[packet_hash(p,n)%VR_PACKET_CACHE_MAX];
    if(!c->control_len||memcmp(c->key,key,sizeof(key)))return NULL;
    if(p[9]==6&&(p[(p[0]&15u)*4u+13]&2)){c->control_len=0;return NULL;}
    if(c->revision!=e->policy_revision||c->count<2||c->count>VR_PATH_HOPS+1||c->link<0||(size_t)c->link>=e->link_count)return NULL;
    vr_link *l=&e->links[c->link];if(!l->ssl||l->session!=c->session)return NULL;
    if(c->binding>=0&&c->binding<VR_NAT_MAX) {
        vr_flow_binding *b=&e->bindings[c->binding];
        if(b->hash==c->hash&&b->session==c->session&&b->link==c->link)b->used=vr_millis();
    }
    return c;
}

static int packet_cache_path(vr_engine *e,vr_packet_cache *c,char path[VR_PATH_MAX][129],size_t n)
{
    if(c->count!=n)return 0;
    json_object *nodes=vr_member(e->policy,"probe_ips");
    for(size_t i=0;i<n;i++)if(strcmp(path[i],vr_string(json_object_array_get_idx(nodes,c->nodes[i]),"node_id")))return 0;
    return 1;
}

static void packet_cache_store(vr_engine *e,vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{
    uint8_t key[13];if(n<2||n>VR_PATH_HOPS+1||strcmp(path[0],e->config.node_id)||f->control_len>128||!packet_identity(f->data,f->data_len,key))return;
    if(f->data[9]==6&&(f->data[(f->data[0]&15u)*4u+13]&2))return;
    vr_packet_cache next={0};json_object *nodes=vr_member(e->policy,"probe_ips");
    for(size_t i=0;i<n;i++) {
        size_t j;for(j=0;j<(size_t)json_object_array_length(nodes)&&j<256;j++)if(!strcmp(path[i],vr_string(json_object_array_get_idx(nodes,j),"node_id")))break;
        if(j>=256||j>=(size_t)json_object_array_length(nodes))return;
        next.nodes[i]=(uint8_t)j;
    }
    memcpy(next.key,key,sizeof(key));memcpy(next.control,f->control,f->control_len);next.control_len=f->control_len;
    next.count=(uint8_t)n;next.link=(int)(l-e->links);next.session=l->session;next.revision=e->policy_revision;
    next.hash=packet_hash(f->data,f->data_len);next.binding=-1;
    for(size_t i=0;i<VR_NAT_MAX;i++)if(e->bindings[i].used&&e->bindings[i].hash==next.hash&&e->bindings[i].link==next.link&&e->bindings[i].session==next.session){next.binding=(int)i;break;}
    e->packet_cache[next.hash%VR_PACKET_CACHE_MAX]=next;
}

static int owner_matches(vr_link *l,const char *user)
{return !user || !strcmp(vr_string(l->rule,"user_id"),user);}

static vr_link *best_route_for_user(vr_engine *e,const char *peer,const char *user)
{
    vr_link *best=NULL;
    for(size_t i=0;i<e->link_count;i++) {
        vr_link *l=&e->links[i];if(!l->ssl||strcmp(peer,l->peer)||!owner_matches(l,user))continue;
        if(!best || (l->rtt && (!best->rtt || l->rtt<best->rtt)) ||
           (l->rtt==best->rtt && strcmp(l->route,best->route)<0))best=l;
    }
    return best;
}

static vr_link *best_route(vr_engine *e,const char *peer)
{return best_route_for_user(e,peer,NULL);}

static vr_link *carrier_select_for_user(vr_engine *e,const char *peer,const vr_frame *f,const char *user)
{
    uint32_t hash=0;int flow=flow_hash(f,&hash);vr_flow_binding *oldest=&e->bindings[0];
    if(flow)for(size_t i=0;i<VR_NAT_MAX;i++) {
        vr_flow_binding *b=&e->bindings[i];
        if(b->used&&b->hash==hash&&b->link>=0&&(size_t)b->link<e->link_count) {
            vr_link *l=&e->links[b->link];
            if(l->ssl&&l->session==b->session&&!strcmp(peer,l->peer)&&owner_matches(l,user)){b->used=vr_millis();return l;}
        }
        if(b->used<oldest->used)oldest=b;
    }
    vr_link *best=best_route_for_user(e,peer,user);if(!best)return NULL;
    vr_link *active[VR_CARRIER_SLOTS];size_t count=0;
    for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl&&!strcmp(best->route,e->links[i].route)&&owner_matches(&e->links[i],user)&&count<VR_CARRIER_SLOTS)active[count++]=&e->links[i];
    if(!flow)return active[0];
    vr_link *base=best-best->slot,*chosen=active[0];
    for(size_t i=0;i<count;i++)if((unsigned)active[i]->slot==base->next_flow_slot){chosen=active[i];break;}
    base->next_flow_slot=((unsigned)chosen->slot+1)%VR_CARRIER_SLOTS;
    oldest->count=0;oldest->hash=hash;oldest->link=(int)(chosen-e->links);
    oldest->session=chosen->session;oldest->used=vr_millis();return chosen;
}

vr_link *vr_carrier_select(vr_engine *e,const char *peer,const vr_frame *f)
{return carrier_select_for_user(e,peer,f,NULL);}

static int adjacent_for_user(vr_engine *e,const char *a,const char *b,const char *user)
{
    json_object *links=vr_member(e->policy,"topology_rules");
    for(size_t i=0;i<(size_t)json_object_array_length(links);i++) {
        json_object *r=json_object_array_get_idx(links,i);if(!json_object_get_boolean(vr_member(r,"enabled")))continue;
        if(user && strcmp(vr_string(r,"user_id"),user))continue;
        const char *from=vr_string(r,"from_node_id"),*to=vr_string(r,"to_node_id");
        if((!strcmp(from,a)&&!strcmp(to,b))||(!strcmp(from,b)&&!strcmp(to,a)))return 1;
    }
    return 0;
}

static int adjacent(vr_engine *e,const char *a,const char *b)
{return adjacent_for_user(e,a,b,NULL);}

static int path_owned_by_link(vr_engine *e,vr_link *l,char path[VR_PATH_MAX][129],size_t n)
{
    /* A locally generated response can reuse the reverse of this carrier's
       already authorized ingress path. Policy publication clears that cache. */
    if(l->ingress_count==n) {
        size_t i;for(i=0;i<n;i++)if(strcmp(path[i],l->ingress_path[n-i-1]))break;
        if(i==n)return 1;
    }
    const char *user=vr_string(l->rule,"user_id");
    for(size_t i=1;i<n;i++)if(!adjacent_for_user(e,path[i-1],path[i],user))return 0;
    return 1;
}

static vr_link *path_carrier_select(vr_engine *e,const char *peer,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{
    vr_link *chosen=vr_carrier_select(e,peer,f);
    if(!chosen || path_owned_by_link(e,chosen,path,n))return chosen;
    vr_link *best=NULL;
    for(size_t i=0;i<e->link_count;i++) {
        vr_link *l=&e->links[i];
        if(!l->ssl||strcmp(peer,l->peer)||!path_owned_by_link(e,l,path,n))continue;
        if(!best || (l->rtt && (!best->rtt || l->rtt<best->rtt)) ||
           (l->rtt==best->rtt && strcmp(l->route,best->route)<0))best=l;
    }
    return best?carrier_select_for_user(e,peer,f,vr_string(best->rule,"user_id")):NULL;
}

typedef struct {
    vr_engine *e;json_object *nodes;size_t count;int goal,start,dist[256];
    uint8_t current[VR_PATH_HOPS+1],best[VR_PATH_HOPS+1];
    size_t length,candidates;uint64_t rtt;int missing,found;
} path_search;

static void path_candidate(path_search *s)
{
    vr_engine *e=s->e;uint64_t now=vr_millis();vr_path_rtt *record=NULL,*oldest=NULL;
    for(size_t i=0;i<VR_PATH_RTT_MAX;i++) {
        vr_path_rtt *r=&e->path_rtt[i];
        if(r->count==s->length&&!memcmp(r->nodes,s->current,s->length)){record=r;break;}
        if(!*r->id&&(!oldest||r->used<oldest->used))oldest=r;
    }
    if(!record&&oldest){record=oldest;memset(record,0,sizeof(*record));record->count=(uint8_t)s->length;memcpy(record->nodes,s->current,s->length);}
    if(record)record->used=now;
    const char *peer=vr_string(json_object_array_get_idx(s->nodes,s->current[1]),"node_id");
    vr_link *first=best_route(e,peer);uint64_t rtt=first?first->rtt:0;
    int missing=(int)s->length-1-(rtt?1:0);
    if(record&&record->rtt&&now-record->updated<=90000){rtt=record->rtt;missing=0;}
    int less=!s->found||missing<s->missing||(missing==s->missing&&rtt<s->rtt);
    if(s->found&&missing==s->missing&&rtt==s->rtt)for(size_t i=0;i<s->length;i++) {
        int cmp=strcmp(vr_string(json_object_array_get_idx(s->nodes,s->current[i]),"node_id"),vr_string(json_object_array_get_idx(s->nodes,s->best[i]),"node_id"));
        if(cmp){less=cmp<0;break;}
    }
    if(less){memcpy(s->best,s->current,s->length);s->rtt=rtt;s->missing=missing;s->found=1;}
    s->candidates++;
}

static void path_walk(path_search *s,int at,size_t depth)
{
    if(s->candidates>=VR_PATH_RTT_MAX)return;
    s->current[depth]=(uint8_t)at;
    if(at==s->goal){path_candidate(s);return;}
    if(depth+1>=s->length)return;
    const char *from=vr_string(json_object_array_get_idx(s->nodes,(size_t)at),"node_id");
    for(size_t i=0;i<s->count;i++) {
        if((int)i==s->start || s->dist[i]!=(int)(s->length-depth-2))continue;
        const char *to=vr_string(json_object_array_get_idx(s->nodes,i),"node_id");
        if((depth==0&&!connected(s->e,to))||!adjacent(s->e,from,to))continue;
        path_walk(s,(int)i,depth+1);
    }
}

int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n)
{
    memset(path,0,VR_PATH_MAX*129);*n=0;
    if(e->paths_revision!=e->policy_revision){memset(e->path_rtt,0,sizeof(e->path_rtt));e->paths_revision=e->policy_revision;}
    path_search s={0};s.e=e;s.nodes=vr_member(e->policy,"probe_ips");s.count=json_object_array_length(s.nodes);s.start=s.goal=-1;
    if(s.count>256)return -1;
    for(size_t i=0;i<s.count;i++) {
        s.dist[i]=-1;const char *id=vr_string(json_object_array_get_idx(s.nodes,i),"node_id");
        if(!strcmp(id,e->config.node_id))s.start=(int)i;
        if(!strcmp(id,target))s.goal=(int)i;
    }
    if(s.start<0||s.goal<0||s.start==s.goal)return -1;
    /* A live, enabled one-hop edge always wins under hops-first routing. */
    if(connected(e,target)&&adjacent(e,e->config.node_id,target)) {
        s.length=2;s.current[0]=(uint8_t)s.start;s.current[1]=(uint8_t)s.goal;
        path_candidate(&s);
        if(vr_copy(path[0],129,e->config.node_id)||vr_copy(path[1],129,target))return -1;
        *n=2;return 0;
    }
    int queue[256];size_t head=0,tail=0;queue[tail++]=s.goal;s.dist[s.goal]=0;
    while(head<tail) {
        int at=queue[head++];if(at==s.start||s.dist[at]>=VR_PATH_HOPS)continue;
        const char *from=vr_string(json_object_array_get_idx(s.nodes,(size_t)at),"node_id");
        for(size_t i=0;i<s.count;i++)if(s.dist[i]<0&&adjacent(e,from,vr_string(json_object_array_get_idx(s.nodes,i),"node_id"))){s.dist[i]=s.dist[at]+1;queue[tail++]=(int)i;}
    }
    int hops=VR_PATH_HOPS+1;
    for(size_t i=0;i<s.count;i++)if((int)i!=s.start&&s.dist[i]>=0&&s.dist[i]+1<hops) {
        const char *to=vr_string(json_object_array_get_idx(s.nodes,i),"node_id");
        if(connected(e,to)&&adjacent(e,e->config.node_id,to))hops=s.dist[i]+1;
    }
    if(hops>VR_PATH_HOPS)return -1;
    s.length=(size_t)hops+1;path_walk(&s,s.start,0);if(!s.found)return -1;
    for(size_t i=0;i<s.length;i++)if(vr_copy(path[i],129,vr_string(json_object_array_get_idx(s.nodes,s.best[i]),"node_id")))return -1;
    *n=s.length;return 0;
}

int vr_packet_path(vr_engine *e,const char *target,const uint8_t *packet,size_t length,char path[VR_PATH_MAX][129],size_t *n)
{
    uint32_t hash=packet_hash(packet,length);json_object *nodes=vr_member(e->policy,"probe_ips");
    vr_packet_cache *cached=packet_cache_find(e,packet,length);
    if(cached&&!strcmp(target,vr_string(json_object_array_get_idx(nodes,cached->nodes[cached->count-1]),"node_id"))) {
        for(size_t i=0;i<cached->count;i++)vr_copy(path[i],129,vr_string(json_object_array_get_idx(nodes,cached->nodes[i]),"node_id"));
        *n=cached->count;return 0;
    }
    for(size_t i=0;i<VR_NAT_MAX;i++) {
        vr_flow_binding *b=&e->bindings[i];
        if(!b->used||b->hash!=hash||b->revision!=e->policy_revision||b->count<2||b->count>VR_PATH_HOPS+1||b->link<0||(size_t)b->link>=e->link_count)continue;
        vr_link *l=&e->links[b->link];if(!l->ssl||l->session!=b->session)continue;
        int valid=1;for(size_t k=0;k<b->count;k++)if(vr_copy(path[k],129,vr_string(json_object_array_get_idx(nodes,b->nodes[k]),"node_id")))valid=0;
        if(!valid||strcmp(path[0],e->config.node_id)||strcmp(path[b->count-1],target))continue;
        for(size_t k=1;k<b->count;k++)if(!adjacent(e,path[k-1],path[k]))valid=0;
        if(valid){*n=b->count;b->used=vr_millis();return 0;}
    }
    return vr_path(e,target,path,n);
}

void vr_link_close(vr_link *l,const char *error)
{
    if(l->engine)for(size_t i=0;i<VR_PACKET_CACHE_MAX;i++) {
        vr_packet_cache *c=&l->engine->packet_cache[i];
        if(c->control_len&&c->link>=0&&(size_t)c->link<l->engine->link_count&& &l->engine->links[c->link]==l)c->control_len=0;
    }
    if(l->tx_backlog_since){uint64_t span=vr_millis()-l->tx_backlog_since;if(span>l->tx_backlog_peak_ms)l->tx_backlog_peak_ms=span;l->tx_backlog_since=0;}
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
    memset(&l->decoder,0,sizeof(l->decoder));
    l->ingress_count=0;l->rtt=0;l->ping_time=0;*l->ping_id=0;
}

int vr_link_queue(vr_link *l,const void *bytes,size_t n)
{
    if(!l->ssl||!n||n>VR_TX_CAP-sizeof(vr_tx_frame))return -1;
    unsigned lane=(unsigned)queue_class(bytes,n);
    size_t length=n;
    const uint8_t *wire=bytes;
    int small_packet=lane==TX_BUSINESS&&n>=12&&length<=TX_SMALL_PACKET&&
        wire[0]==0x56&&wire[1]==0x52&&wire[2]==0&&wire[3]==1&&wire[4]==0&&wire[5]==1;
    if(small_packet&&!l->tx_head[TX_CONTROL]&&!l->tx_head[TX_BULK]) {
        vr_tx_frame *tail=l->tx_tail[lane];
        /* Only an inactive tail may grow. A dequeued SSL_write retry owns
           its buffer until OpenSSL consumes it. No timer delays a lone packet. */
        if(tail&&tail->capacity==TX_BATCH_BYTES&&tail->count<TX_BATCH_FRAMES&&length<=tail->capacity-tail->length) {
            memcpy(tail->bytes+tail->length,bytes,n);
            tail->length+=n;tail->frame_bytes+=n;tail->count++;
            l->used+=n;l->tx_bytes[lane]+=n;l->tx_enqueued++;
            if(l->used>l->tx_peak)l->tx_peak=l->used;
            return 0;
        }
    }
    size_t capacity=length;
    if(small_packet&&!l->tx_head[TX_CONTROL]&&!l->tx_head[TX_BULK]&&
       sizeof(vr_tx_frame)+TX_BATCH_BYTES<=VR_TX_CAP-l->tx_memory&&
       sizeof(vr_tx_frame)+TX_BATCH_BYTES<=VR_TX_CAP-VR_TX_CONTROL_RESERVE-l->tx_memory_class[TX_BUSINESS])
        capacity=TX_BATCH_BYTES;
    size_t memory=sizeof(vr_tx_frame)+capacity;
    if(memory>VR_TX_CAP-l->tx_memory){l->tx_full++;return -1;}
    if(lane==TX_CONTROL) {
        if(memory>VR_TX_CONTROL_CAP-l->tx_memory_class[lane]){l->tx_full++;return -1;}
    } else {
        size_t business=l->tx_memory_class[TX_BUSINESS]+l->tx_memory_class[TX_BULK];
        if(memory>VR_TX_CAP-VR_TX_CONTROL_RESERVE-business){l->tx_full++;return -1;}
        if(lane==TX_BULK&&memory>VR_TX_BULK_CAP-l->tx_memory_class[lane]){l->tx_full++;return -1;}
    }
    vr_tx_frame *f=malloc(memory);if(!f)return -1;
    memcpy(f->bytes,bytes,n);
    f->next=NULL;f->length=length;f->capacity=capacity;f->lane=lane;f->count=1;f->enqueued=vr_millis();
    f->frame_bytes=n;
    if(l->tx_tail[lane])l->tx_tail[lane]->next=f;else l->tx_head[lane]=f;
    l->tx_tail[lane]=f;l->used+=length;l->tx_bytes[lane]+=length;
    if(!l->tx_memory)l->tx_backlog_since=f->enqueued;
    l->tx_memory+=memory;l->tx_memory_class[lane]+=memory;l->tx_enqueued++;
    if(l->used>l->tx_peak)l->tx_peak=l->used;
    if(l->tx_memory>l->tx_memory_peak)l->tx_memory_peak=l->tx_memory;
    return 0;
}

static int link_frame(vr_link *l,const vr_frame *f)
{
    if(!l||!l->ssl)return -1;
    uint8_t *b=malloc(12+f->control_len+f->data_len);size_t len;if(!b)return -1;
    int rc=vr_frame_encode(f,b,12+f->control_len+f->data_len,&len);
    if(!rc)rc=vr_link_queue(l,b,len);
    free(b);return rc;
}

int vr_link_send_json(vr_link *l,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,json_object *m)
{
    json_object *o=json_object_new_object(),*a=json_object_new_array();
    for(size_t i=0;i<n;i++)json_object_array_add(a,json_object_new_string(path[i]));
    json_object_object_add(o,"path",a);
    const char *control=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN),*data=json_object_to_json_string_ext(m,JSON_C_TO_STRING_PLAIN);
    vr_frame f={main,sub,(const uint8_t *)control,(const uint8_t *)data,strlen(control),strlen(data)};
    int rc=link_frame(l,&f);json_object_put(o);return rc;
}

static int send_frame(vr_engine *e,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n,const char *user)
{
    size_t at;for(at=0;at<n;at++)if(!strcmp(path[at],e->config.node_id))break;
    if(at+1>=n)return -1;
    vr_link *l=user?carrier_select_for_user(e,path[at+1],f,user):path_carrier_select(e,path[at+1],f,path,n);
    /* A mixed-version relay translates only its path control, never payload. */
    if(l&&!l->binary_path&&f->control_len&&f->control[0]==0) {
        json_object *o=json_object_new_object(),*a=json_object_new_array();
        for(size_t i=0;i<n;i++)json_object_array_add(a,json_object_new_string(path[i]));
        json_object_object_add(o,"path",a);const char *control=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);
        vr_frame legacy=*f;legacy.control=(const uint8_t *)control;legacy.control_len=strlen(control);
        int rc=link_frame(l,&legacy);json_object_put(o);return rc;
    }
    return link_frame(l,f);
}

int vr_send(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,const void *data,size_t length)
{
    if(n<2||n>VR_PATH_MAX)return -1;
    vr_frame f={main,sub,NULL,data,0,length};
    if(main==1&&sub==1) {
        vr_packet_cache *c=packet_cache_find(e,data,length);
        if(c&&packet_cache_path(e,c,path,n)) {
            f.control=c->control;f.control_len=c->control_len;
            return link_frame(&e->links[c->link],&f);
        }
    }
    size_t at;for(at=0;at<n;at++)if(!strcmp(path[at],e->config.node_id))break;
    if(at+1>=n)return -1;
    vr_link *selected=path_carrier_select(e,path[at+1],&f,path,n);if(!selected)return -1;
    json_object *o=NULL;uint8_t binary[6+VR_PATH_MAX*130];
    if(selected->binary_path) {
        if(vr_path_binary(path,n,binary,sizeof(binary),&f.control_len))return -1;
        f.control=binary;
    } else {
        o=json_object_new_object();json_object *a=json_object_new_array();
        for(size_t i=0;i<n;i++)json_object_array_add(a,json_object_new_string(path[i]));
        json_object_object_add(o,"path",a);
        const char *control=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);f.control=(const uint8_t *)control;f.control_len=strlen(control);
    }
    int rc=link_frame(selected,&f);
    if(!rc&&main==1&&sub==1)packet_cache_store(e,selected,&f,path,n);
    if(!rc&&main==1&&n<=VR_PATH_HOPS+1&&!strcmp(path[0],e->config.node_id)) {
        uint32_t hash=packet_hash(data,length);json_object *nodes=vr_member(e->policy,"probe_ips");
        for(size_t i=0;i<VR_NAT_MAX;i++)if(e->bindings[i].used&&e->bindings[i].hash==hash&&e->bindings[i].link==(int)(selected-e->links)&&e->bindings[i].session==selected->session) {
            vr_flow_binding *b=&e->bindings[i];b->count=0;b->revision=e->policy_revision;int valid=1;
            for(size_t k=0;k<n;k++){size_t j;for(j=0;j<(size_t)json_object_array_length(nodes);j++)if(!strcmp(path[k],vr_string(json_object_array_get_idx(nodes,j),"node_id")))break;if(j>=256||j>=(size_t)json_object_array_length(nodes)){valid=0;break;}b->nodes[k]=(uint8_t)j;}
            if(valid)b->count=(uint8_t)n;
            break;
        }
    }
    json_object_put(o);return rc;
}

int vr_send_json(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,json_object *m)
{const char *s=json_object_to_json_string_ext(m,JSON_C_TO_STRING_PLAIN);return vr_send(e,main,sub,path,n,s,strlen(s));}

static int link_receive_impl(const vr_frame *f,void *arg)
{
    vr_link *l=arg;vr_engine *e=l->engine;char path[VR_PATH_MAX][129];size_t count,at;
    if(f->main==VR_KEEPALIVE_MAIN) {
        if(f->control_len||f->data_len)return -1;
        if(f->sub==VR_KEEPALIVE_PING)return vr_link_keepalive(l,VR_KEEPALIVE_PONG);
        return f->sub==VR_KEEPALIVE_PONG?0:-1;
    }
    const char *rejected=NULL;
    if(l->ingress_count && f->control_len==l->ingress_length &&
       (!f->control_len || !memcmp(f->control,l->ingress_control,f->control_len))) {
        count=l->ingress_count;at=l->ingress_at;memcpy(path,l->ingress_path,count*129);goto accepted;
    }
    /* Probe Node permits an omitted/empty path and uses this carrier's ingress
       path. Malformed JSON and explicit invalid paths still fail closed. */
    json_object *control=f->control_len&&f->control[0]!=0?vr_json_parse(f->control,f->control_len):NULL;
    json_object *a=vr_member(control,"path");
    int fallback=(!f->control_len || (control && json_object_is_type(control,json_type_object) &&
        (!a || json_object_is_type(a,json_type_null) || (json_object_is_type(a,json_type_array)&&!json_object_array_length(a)))));
    json_object_put(control);
    if(fallback){memset(path,0,sizeof(path));strcpy(path[0],l->peer);strcpy(path[1],e->config.node_id);count=2;}
    else if(vr_frame_path(f,path,&count)){rejected="invalid business path";goto drop;}
    if(count>VR_PATH_HOPS+1){rejected="path exceeds maximum hops";goto drop;}
    for(at=0;at<count;at++)if(!strcmp(path[at],e->config.node_id))break;
    if(!at || at>=count || strcmp(path[at-1],l->peer)){rejected="ingress peer mismatch";goto drop;}
    for(size_t i=1;i<count;i++)if(!adjacent_for_user(e,path[i-1],path[i],vr_string(l->rule,"user_id"))){rejected="unauthorized path edge";goto drop;}
    if(f->control_len<=sizeof(l->ingress_control)) {
        if(f->control_len)memcpy(l->ingress_control,f->control,f->control_len);
        l->ingress_length=f->control_len;l->ingress_count=count;l->ingress_at=at;memcpy(l->ingress_path,path,count*129);
    }
accepted:
    l->frames_received++;l->frame_bytes_received+=12+f->control_len+f->data_len;
    if(at+1<count){send_frame(e,f,path,count,vr_string(l->rule,"user_id"));return 0;}
    vr_link_frame(l,f,path,count);return 0;
drop:;
    /* Match Probe Node: reject the business frame, not its healthy carrier. */
    char event[240];snprintf(event,sizeof(event),"Peer %.32s dropped frame %u/%u: %s",l->peer,f->main,f->sub,rejected);
    vr_service_log(e->service,"link",event);return 0;
}

int vr_link_receive(const vr_frame *f,void *arg)
{vr_link *l=arg;uint64_t t=vr_observe_begin(l->engine,VR_OBS_FRAME_RX);int rc=link_receive_impl(f,arg);vr_observe_end(l->engine,VR_OBS_FRAME_RX,t,0,0);return rc;}

int vr_link_keepalive(vr_link *l,uint16_t subtype)
{
    vr_frame frame={VR_KEEPALIVE_MAIN,subtype,NULL,NULL,0,0};
    uint8_t wire[12];size_t length;
    if(vr_frame_encode(&frame,wire,sizeof(wire),&length))return -1;
    return vr_link_queue(l,wire,length);
}

static void link_read(vr_link *l)
{
    /* Drain a bounded batch. Reading one record per 50 ms poll can leave
       decrypted records stranded in OpenSSL and stall a remote download. */
    uint64_t start=vr_millis();
    for(unsigned batch=0;batch<16;batch++) {
        ERR_clear_error();errno=0;
        uint8_t bytes[16384];int n=SSL_read(l->ssl,bytes,sizeof(bytes));
        if(n>0){l->rx+=(unsigned)n;l->last_rx=vr_millis();*l->error=0;
            int rc=vr_decoder_feed(&l->decoder,bytes,(size_t)n,vr_link_receive,l);
            if(rc){char detail[96];snprintf(detail,sizeof(detail),"invalid direct carrier frame (%d)",rc);vr_link_close(l,detail);return;}}
        else {int error=SSL_get_error(l->ssl,n);if(error!=SSL_ERROR_WANT_READ&&error!=SSL_ERROR_WANT_WRITE){char detail[96];vr_tls_error(detail,sizeof(detail),"receive",error);vr_link_close(l,detail);return;}break;}
        if(vr_millis()-start>=2)break;
    }
}

static void link_write(vr_link *l)
{
    /* Bound work across peers; only preempt between whole frame batches. */
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

void vr_links_close_failed(vr_link *links, size_t count, const struct pollfd *fds)
{
    for (size_t i = 0; i < count; i++) {
        if (links[i].ssl && (fds[i].revents & (POLLERR | POLLHUP | POLLNVAL)))
            vr_link_close(&links[i], "connection closed");
    }
}

void vr_links_poll(vr_link *links, size_t count, const struct pollfd *fds)
{
    /* The TUN batch can enqueue onto a previously empty carrier after poll.
       Give that carrier one nonblocking write before processing incoming TLS. */
    for (size_t i = 0; i < count; i++) {
        vr_link *l = &links[i];
        if (!l->ssl) continue;
        short interest = l->write_events ? l->write_events : POLLOUT;
        int newly_queued = !l->write_events && !(fds[i].events & POLLOUT);
        if (l->used && (newly_queued || (fds[i].revents & interest)))
            link_write(l);
    }

    /* Each readable carrier gets a turn even if another writer hit EAGAIN.
       A write waiting for TLS handshake input owns that read readiness. */
    for (size_t i = 0; i < count; i++) {
        vr_link *l = &links[i];
        if (l->ssl && l->write_events != POLLIN &&
            ((fds[i].revents & POLLIN) || SSL_pending(l->ssl)))
            link_read(l);
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
    char auth[300],nonce_mac[128],source[129],role[32],ticket[8193],mode[32];
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
    return 0;
}

static int source_ip(int fd,vr_ip *ip)
{
    struct sockaddr_storage sa;socklen_t n=sizeof(sa);char text[46];
    if(getpeername(fd,(struct sockaddr *)&sa,&n))return -1;
    const void *address=sa.ss_family==AF_INET?(const void *)&((struct sockaddr_in *)&sa)->sin_addr:
        sa.ss_family==AF_INET6?(const void *)&((struct sockaddr_in6 *)&sa)->sin6_addr:NULL;
    if(!address||!inet_ntop(sa.ss_family,address,text,sizeof(text)))return -1;
    return vr_ip_parse(text,ip);
}

static vr_link *accept_blocking(vr_engine *e,int fd)
{
    vr_ip ip;if(source_ip(fd,&ip)||vr_service_ip_blocked(e->service,&ip,vr_millis())){close(fd);return NULL;}
    uint64_t deadline=vr_millis()+4000;SSL *ssl=vr_tls_accept(e->tls,fd,deadline);if(!ssl){close(fd);return NULL;}
    char *header=malloc(16384);char route[64];vr_link *l=NULL;time_t expiry;
    const char *reason="invalid private TLS upgrade";
    if(!header || vr_http_header(ssl,header,16384,deadline))goto fail;
    int ordinary=vr_http_camouflage(ssl,header,deadline);
    if(ordinary>0){free(header);SSL_free(ssl);close(fd);return NULL;}
    if(ordinary<0||!header_equals(header,"Upgrade",VR_DIRECT_UPGRADE)||
       !vr_http_token(header,"Connection","Upgrade")||strcasestr(header,"\r\nTransfer-Encoding:")||
       strcasestr(header,"\r\nContent-Length:")||!vr_http_field(header,"X-Codex-Route-Id",route,sizeof(route))||
       !relay_request_line(header,route))goto fail;
    int slot=vr_carrier_slot(header);if(slot<0)goto fail;
    for(size_t i=0;i<e->link_count;i++)if(!strcmp(route,e->links[i].route)&&e->links[i].slot==slot){l=&e->links[i];break;}
    if(!l){reason="route or carrier slot not configured";goto fail;}
    pthread_mutex_lock(&e->mutex);int auth=accept_auth(l,header,&expiry);pthread_mutex_unlock(&e->mutex);
    vr_service_ip_result(e->service,&ip,!auth,vr_millis());
    if(auth){reason="HMAC or route ticket rejected";goto fail;}
    reason="private TLS response failed";
    const char reply[]="HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " VR_DIRECT_UPGRADE "\r\n\r\n";
    if(vr_tls_io(ssl,1,(void *)reply,sizeof(reply)-1,deadline))goto fail;
    vr_link *result=calloc(1,sizeof(*result));if(!result)goto fail;
    result->engine=e;result->rule=l->rule;result->slot=slot;result->binary_path=1;strcpy(result->route,l->route);strcpy(result->peer,l->peer);
    result->ssl=ssl;result->fd=fd;result->client=0;result->last_rx=vr_millis();result->auth_expires=expiry;free(header);return result;
fail:;
    pthread_mutex_lock(&e->mutex);uint64_t now=vr_millis();int report=!e->auth_reject_log||now-e->auth_reject_log>=10000;
    if(report)e->auth_reject_log=now;
    pthread_mutex_unlock(&e->mutex);
    if(report){char address[46],event[240];vr_ip_text(&ip,address);snprintf(event,sizeof(event),"Incoming carrier rejected: IP %s peer %.32s: %s",address,l?l->peer:"unknown",reason);vr_service_log(e->service,"link",event);}
    free(header);SSL_free(ssl);close(fd);return NULL;
}

static void dial_blocking(vr_link *l)
{
    vr_engine *e=l->engine;vr_addresses addresses=vr_engine_addresses(e);json_object *r=l->rule;
    if(l->ssl || strcmp(vr_string(r,"from_node_id"),e->config.node_id) || vr_millis()<l->retry)return;
    l->retry=vr_millis()+10000;
    const char *host=vr_string(r,"to_service_domain"),*pin=vr_string(r,"to_tls_spki_sha256");int port=json_object_get_int(vr_member(r,"to_service_port"));
    port=vr_policy_service_port(e->policy,vr_string(r,"to_node_id"),port);
    time_t expires;
    if(vr_ticket_verify(vr_string(r,"auth_ticket"),vr_string(r,"user_public_key"),l->route,vr_string(r,"from_node_id"),vr_string(r,"to_node_id"),time(NULL),&expires)){strcpy(l->error,"route ticket invalid or expired");return;}
    uint64_t deadline=vr_millis()+5000;int fd=-1;char event[240];
    SSL *ssl=vr_tls_connect_relay(host,port,pin,&e->config,addresses.wan_if,&fd,deadline);if(!ssl){strcpy(l->error,"DNS/TCP/TLS connection failed");
        snprintf(event,sizeof(event),"Peer %.32s %.120s:%d: %.64s",l->peer,host,port,l->error);vr_service_log(e->service,"link",event);return;}
    char nonce[49],text[1024],mac[65];
    char *request=malloc(16384),*response=malloc(4096);
    if(!request || !response || vr_random_hex(nonce,24))goto fail;
    snprintf(text,sizeof(text),"%s\n%s\nGET\n%s\n%s\nto_next",l->route,nonce,VR_RELAY_PATH,e->config.node_id);
    if(vr_hmac(vr_string(r,"secret"),text,mac))goto fail;
    char endpoint[280];if(vr_hostport(host,port,endpoint,sizeof(endpoint)))goto fail;
    int len=snprintf(request,16384,"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: " VR_DIRECT_UPGRADE "\r\nConnection: Upgrade\r\nAuthorization: Bearer %s\r\nX-Codex-Route-Id: %s\r\nX-Codex-Api-Version: 2025-03-22\r\nX-Codex-Auth-Mode: secret_hmac\r\nX-Codex-Relay-Mode: bridge\r\nX-Codex-Relay-Role: to_next\r\nX-Codex-Source-Node-Id: %s\r\nX-Codex-User-Auth-Ticket: %s\r\nX-Codex-VRouter-Carrier-Slot: %d\r\nX-Codex-Mac: %s\r\n\r\n",VR_RELAY_PATH,endpoint,nonce,l->route,e->config.node_id,vr_string(r,"auth_ticket"),l->slot,mac);
    if(len<0||len>=16384 || vr_tls_io(ssl,1,request,(size_t)len,deadline) || vr_http_header(ssl,response,4096,deadline) || strncmp(response,"HTTP/1.1 101 ",13) ||
        !header_equals(response,"Upgrade",VR_DIRECT_UPGRADE)||!vr_http_token(response,"Connection","Upgrade"))goto fail;
    l->binary_path=1;l->ssl=ssl;l->fd=fd;l->client=1;l->last_rx=vr_millis();l->auth_expires=expires;*l->error=0;free(request);free(response);
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
    int index=(int)(l-e->links);
    for(size_t i=0;i<2;i++)if(e->handshakes[i].started&&e->handshakes[i].index==index)return;
    if(!start_handshake(e,-1,index))l->retry=vr_millis()+10000;
}

void vr_link_accept(vr_engine *e,int fd)
{
    vr_ip ip;
    if(source_ip(fd,&ip)||vr_service_ip_blocked(e->service,&ip,vr_millis())||start_handshake(e,fd,-1))close(fd);
}

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
                vr_tcp_windows_start(&l->windows,l->fd,vr_millis(),l->rx,l->tx);
                l->last_rx=result->last_rx;l->auth_expires=result->auth_expires;
                l->binary_path=result->binary_path;l->keepalive_at=vr_millis();l->connected_at=l->keepalive_at;l->session=++e->carrier_session;l->opens++;*l->error=0;
                result->ssl=NULL;result->fd=-1;
            } else if(!l->ssl){snprintf(l->error,sizeof(l->error),"%s",result->error);l->retry=vr_millis()+10000;}
            break;
        }
        vr_link_close(result,"");free(result);job->result=NULL;
    }
}
