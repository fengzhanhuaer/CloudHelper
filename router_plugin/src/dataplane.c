#define _POSIX_C_SOURCE 200809L
#include "engine.h"
#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static void inject(vr_engine *e,const void *b,size_t n)
{
    ssize_t written=write(e->tun,b,n);
    if(written!=(ssize_t)n)snprintf(e->error,sizeof(e->error),"TUN receive queue unavailable");
}

static int node_for_ip(vr_engine *e,uint32_t ip,char id[129])
{
    json_object *nodes=vr_member(e->policy,"probe_ips");
    for(size_t i=0;i<(size_t)json_object_array_length(nodes);i++) {
        json_object *node=json_object_array_get_idx(nodes,i);struct in_addr a;
        if(inet_pton(AF_INET,vr_string(node,"ip"),&a)==1 && a.s_addr==ip)return vr_copy(id,129,vr_string(node,"node_id"));
    }
    return -1;
}

static int fake_ip(vr_engine *e,const char *ip)
{
    char base[64];if(vr_copy(base,sizeof(base),e->cidr))return 0;
    char *slash=strchr(base,'/');if(!slash)return 0;*slash++=0;unsigned bits=(unsigned)atoi(slash);
    struct in_addr a,b;if(bits>32||inet_pton(AF_INET,ip,&a)!=1||inet_pton(AF_INET,base,&b)!=1)return 0;
    uint32_t mask=bits?0xffffffffu<<(32-bits):0;char id[129];
    return (ntohl(a.s_addr)&mask)==(ntohl(b.s_addr)&mask) && node_for_ip(e,a.s_addr,id)!=0;
}

static int same(const vr_packet *a,const vr_packet *b)
{return a->src==b->src&&a->dst==b->dst&&a->sport==b->sport&&a->dport==b->dport&&a->protocol==b->protocol;}

static vr_nat *nat_forward(vr_engine *e,const vr_packet *p,int entry,uint32_t source,uint32_t destination)
{
    uint64_t now=vr_millis();vr_nat *free_slot=NULL;
    for(size_t i=0;i<VR_NAT_MAX;i++) {
        vr_nat *f=&e->flows[i];
        if(!f->used || now-f->used>120000){if(!free_slot)free_slot=f;continue;}
        if(f->entry==entry&&same(p,&f->original)){f->used=now;return f;}
    }
    if(!free_slot)return NULL;
    free_slot->original=*p;free_slot->wire=*p;free_slot->entry=entry;free_slot->used=now;
    free_slot->wire.src=source;free_slot->wire.dst=destination;
    free_slot->wire.sport=(uint16_t)(20000+(free_slot-e->flows));
    if(p->protocol==1)free_slot->wire.dport=free_slot->wire.sport;
    return free_slot;
}

static vr_nat *nat_reverse(vr_engine *e,const vr_packet *p)
{
    uint64_t now=vr_millis();
    for(size_t i=0;i<VR_NAT_MAX;i++) {
        vr_nat *f=&e->flows[i];vr_packet *w=&f->wire;
        if(f->used && now-f->used<=120000 && p->protocol==w->protocol && p->src==w->dst && p->dst==w->src && p->sport==w->dport && p->dport==w->sport){f->used=now;return f;}
    }
    return NULL;
}

static int relay(vr_engine *e,const char *target,uint8_t *b,size_t n)
{char path[VR_PATH_MAX][129];size_t count;return vr_path(e,target,path,&count)?-1:vr_send(e,1,1,path,count,b,n);}

static int public4(const char *ip)
{
    struct in_addr a;if(inet_pton(AF_INET,ip,&a)!=1)return 0;uint32_t v=ntohl(a.s_addr);
    return v>=0x01000000u && v<0xe0000000u && (v>>24)!=10 && (v>>24)!=127 &&
        (v&0xffff0000u)!=0xa9fe0000u && (v&0xfff00000u)!=0xac100000u &&
        (v&0xffff0000u)!=0xc0a80000u && (v&0xffc00000u)!=0x64400000u &&
        (v&0xfffe0000u)!=0xc6120000u;
}

static int mapping_destination(vr_engine *e,const vr_mapping *m,uint32_t *destination)
{
    if(m->resolved){*destination=m->resolved;return 0;}
    char ip[16];if(vr_resolve4(m->domain,e->config.dns_upstream,e->addresses.wan_if,ip) || !public4(ip) ||
        !strcmp(ip,e->addresses.lan) || !strcmp(ip,e->addresses.wan))return -1;
    struct in_addr a;inet_pton(AF_INET,ip,&a);*destination=a.s_addr;return 0;
}

/* Forwarding never waits for controller HTTP or upstream DNS. A single bounded
   resolver retains at most 16 first packets; the network loop owns all flows. */
static int packet_mapping(vr_engine *e,const char *ip,vr_mapping *out)
{
    int found=0;pthread_mutex_lock(&e->mutex);
    for(size_t i=0;i<VR_MAPPING_MAX;i++)if(e->mappings[i].expires>time(NULL)&&!strcmp(e->mappings[i].ip,ip)){
        *out=e->mappings[i];found=1;break;}
    pthread_mutex_unlock(&e->mutex);return found?0:-1;
}

static void resolve_later(vr_engine *e,const char *ip,const uint8_t *b,size_t n,char path[VR_PATH_MAX][129],size_t count)
{
    if(!e->resolve_started||n>1500||count>VR_PATH_MAX)return;
    pthread_mutex_lock(&e->mutex);
    for(size_t i=0;i<16;i++)if(!e->resolves[i].state){
        vr_resolve_job *j=&e->resolves[i];memset(j,0,sizeof(*j));j->state=1;j->incoming=path!=NULL;
        strcpy(j->ip,ip);memcpy(j->packet,b,n);j->n=n;j->count=count;
        if(path)memcpy(j->path,path,count*129);
        pthread_cond_signal(&e->resolve_changed);break;}
    pthread_mutex_unlock(&e->mutex);
}

static int cached_destination(const vr_mapping *m,uint32_t *destination)
{
    if(m->resolved){*destination=m->resolved;return 0;}
    struct in_addr numeric;
    if(inet_pton(AF_INET,m->domain,&numeric)==1&&public4(m->domain)){*destination=numeric.s_addr;return 0;}
    return -1;
}

static void *resolver(void *arg)
{
    vr_engine *e=arg;
    for(;;){
        vr_mapping_flush(e,0);
        pthread_mutex_lock(&e->mutex);size_t slot;
        for(;;){for(slot=0;slot<16;slot++)if(e->resolves[slot].state==1)break;
            if(e->resolve_stop||slot<16)break;
            struct timespec wake;clock_gettime(CLOCK_REALTIME,&wake);wake.tv_sec++;
            if(pthread_cond_timedwait(&e->resolve_changed,&e->mutex,&wake))break;}
        if(e->resolve_stop){pthread_mutex_unlock(&e->mutex);break;}
        if(slot==16){pthread_mutex_unlock(&e->mutex);continue;}
        vr_resolve_job *j=&e->resolves[slot];j->state=2;char ip[16];strcpy(ip,j->ip);pthread_mutex_unlock(&e->mutex);
        vr_mapping m;vr_decision none={0};int ok=!vr_mapping_get(e,NULL,ip,none,&m);uint32_t real=0;
        if(ok&&!strcmp(m.exit,e->config.node_id))ok=!mapping_destination(e,&m,&real);
        pthread_mutex_lock(&e->mutex);
        if(ok&&real)for(size_t i=0;i<VR_MAPPING_MAX;i++)if(!strcmp(e->mappings[i].ip,m.ip)&&!strcmp(e->mappings[i].domain,m.domain))e->mappings[i].resolved=real;
        j->state=ok?3:0;pthread_mutex_unlock(&e->mutex);
    }
    return NULL;
}

int vr_dataplane_start(vr_engine *e)
{
    if(pthread_cond_init(&e->resolve_changed,NULL))return -1;
    pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,256*1024);
    int rc=pthread_create(&e->resolve_thread,&attr,resolver,e);pthread_attr_destroy(&attr);
    if(rc){pthread_cond_destroy(&e->resolve_changed);return -1;}e->resolve_started=1;return 0;
}

void vr_dataplane_tick(vr_engine *e)
{
    for(size_t i=0;i<16;i++){
        vr_resolve_job j;pthread_mutex_lock(&e->mutex);int ready=e->resolves[i].state==3;
        if(ready){j=e->resolves[i];e->resolves[i].state=0;}pthread_mutex_unlock(&e->mutex);
        if(!ready)continue;
        if(j.incoming){vr_frame f={1,1,NULL,j.packet,0,j.n};vr_incoming(e,&f,j.path,j.count);}
        else vr_outgoing(e,j.packet,j.n);
    }
}

void vr_dataplane_stop(vr_engine *e)
{
    if(!e->resolve_started)return;
    pthread_mutex_lock(&e->mutex);e->resolve_stop=1;pthread_cond_signal(&e->resolve_changed);pthread_mutex_unlock(&e->mutex);
    pthread_join(e->resolve_thread,NULL);pthread_cond_destroy(&e->resolve_changed);e->resolve_started=0;
    vr_mapping_flush(e,1);
}

void vr_outgoing(vr_engine *e,uint8_t *b,size_t n)
{
    vr_packet p;if(vr_packet_parse(b,n,&p))return;
    vr_nat *reverse=nat_reverse(e,&p);
    if(reverse) {
        vr_packet *o=&reverse->original;
        if(vr_packet_rewrite(b,n,o->dst,o->src,o->dport,o->sport))return;
        if(reverse->entry){inject(e,b,n);return;}
        char target[129];if(!node_for_ip(e,o->src,target))relay(e,target,b,n);
        return;
    }
    char dest[16],target[129];struct in_addr a={p.dst};inet_ntop(AF_INET,&a,dest,sizeof(dest));
    struct in_addr own;inet_pton(AF_INET,e->own_ip,&own);
    if(!node_for_ip(e,p.dst,target)) {
        if(p.src==own.s_addr){relay(e,target,b,n);
        return;}
        vr_nat *f=nat_forward(e,&p,1,own.s_addr,p.dst);if(!f)return;
        if(!vr_packet_rewrite(b,n,f->wire.src,f->wire.dst,f->wire.sport,f->wire.dport))relay(e,target,b,n);
        return;
    }
    if(!fake_ip(e,dest)) {
        vr_decision d=vr_policy_match(e->policy,"",dest);
        if(d.action!=VR_PROXY || !*d.exit_node || !strcmp(d.exit_node,e->config.node_id))return;
        vr_nat *f=nat_forward(e,&p,1,own.s_addr,p.dst);if(!f)return;
        if(!vr_packet_rewrite(b,n,f->wire.src,f->wire.dst,f->wire.sport,f->wire.dport))relay(e,d.exit_node,b,n);
        return;
    }
    vr_mapping m;if(packet_mapping(e,dest,&m)){resolve_later(e,dest,b,n,NULL,0);return;}
    if(!strcmp(m.exit,e->config.node_id)) {
        uint32_t real;if(cached_destination(&m,&real)){resolve_later(e,dest,b,n,NULL,0);return;}
        if(real==inet_addr(e->addresses.lan)||real==inet_addr(e->addresses.wan))return;
        vr_nat *f=nat_forward(e,&p,1,own.s_addr,real);if(f&&!vr_packet_rewrite(b,n,f->wire.src,f->wire.dst,f->wire.sport,f->wire.dport))inject(e,b,n);
    } else {
        vr_nat *f=nat_forward(e,&p,1,own.s_addr,p.dst);if(!f)return;
        if(!vr_packet_rewrite(b,n,f->wire.src,f->wire.dst,f->wire.sport,f->wire.dport))relay(e,m.exit,b,n);
    }
}

void vr_incoming(vr_engine *e,const vr_frame *frame,char path[VR_PATH_MAX][129],size_t count)
{
    if(count<2 || count>VR_PATH_MAX || strcmp(path[count-1],e->config.node_id) || frame->sub!=1 || frame->data_len>1500)return;
    uint8_t b[1500];memcpy(b,frame->data,frame->data_len);vr_packet p;if(vr_packet_parse(b,frame->data_len,&p))return;
    vr_nat *reverse=nat_reverse(e,&p);
    if(reverse&&reverse->entry) {
        vr_mapping m;char fake[16];struct in_addr addr={p.src};inet_ntop(AF_INET,&addr,fake,sizeof(fake));
        char source[129];int allowed=!node_for_ip(e,p.src,source)&&!strcmp(source,path[0]);
        if(!allowed&&fake_ip(e,fake)){
            if(packet_mapping(e,fake,&m)){resolve_later(e,fake,b,frame->data_len,path,count);return;}
            allowed=!strcmp(m.exit,path[0]);}
        if(!allowed){vr_decision d=vr_policy_match(e->policy,"",fake);allowed=d.action==VR_PROXY && !strcmp(d.exit_node,path[0]);}
        if(!allowed)return;
        vr_packet *o=&reverse->original;
        if(!vr_packet_rewrite(b,frame->data_len,o->dst,o->src,o->dport,o->sport))inject(e,b,frame->data_len);
        return;
    }
    char source[129];if(node_for_ip(e,p.src,source)||strcmp(source,path[0]))return;
    struct in_addr own;inet_pton(AF_INET,e->own_ip,&own);
    if(p.dst==own.s_addr){inject(e,b,frame->data_len);
        return;}
    char fake[16];struct in_addr addr={p.dst};inet_ntop(AF_INET,&addr,fake,sizeof(fake));
    if(!fake_ip(e,fake)) {
        if(!vr_gateway_allowed(&e->config.gateway,source)||!vr_gateway_contains(&e->config.gateway,fake))return;
        vr_nat *f=nat_forward(e,&p,0,p.src,p.dst);
        if(f&&!vr_packet_rewrite(b,frame->data_len,f->wire.src,f->wire.dst,f->wire.sport,f->wire.dport))inject(e,b,frame->data_len);
        return;
    }
    vr_mapping m;if(packet_mapping(e,fake,&m)){resolve_later(e,fake,b,frame->data_len,path,count);return;}
    if(strcmp(m.exit,e->config.node_id))return;
    /* Reuse established destination to avoid DNS on every packet. */
    uint32_t real=0;uint64_t now=vr_millis();
    for(size_t i=0;i<VR_NAT_MAX;i++)if(e->flows[i].used&&now-e->flows[i].used<=120000&&!e->flows[i].entry&&same(&p,&e->flows[i].original)){real=e->flows[i].wire.dst;break;}
    if(!real&&cached_destination(&m,&real)){resolve_later(e,fake,b,frame->data_len,path,count);return;}
    if(real==inet_addr(e->addresses.lan)||real==inet_addr(e->addresses.wan))return;
    vr_nat *f=nat_forward(e,&p,0,p.src,real);
    if(f&&!vr_packet_rewrite(b,frame->data_len,f->wire.src,f->wire.dst,f->wire.sport,f->wire.dport))inject(e,b,frame->data_len);
}
