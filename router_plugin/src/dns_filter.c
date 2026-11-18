#define _GNU_SOURCE
#include "engine.h"
#include <poll.h>
#include <stdlib.h>
#include <string.h>

#define FILTER_IPS (VR_DNS_BLACKLIST_MAX*4)
struct vr_dns_filter_state {
    vr_dns_filter wanted;
    char ips[FILTER_IPS][46];size_t count;
    unsigned epoch,serial,applied;
    int stop,started;pthread_t thread;
    vr_addresses addresses;
    uint64_t retry,check;
};
static void add_ip(char ips[][46],size_t *count,const char *ip)
{
    for(size_t i=0;i<*count;i++)if(!strcmp(ips[i],ip))return;
    if(*count<FILTER_IPS)vr_copy(ips[(*count)++],46,ip);
}
static void literal_ips(const vr_dns_filter *f,char ips[][46],size_t *count)
{
    *count=0;memset(ips,0,FILTER_IPS*46);
    if(f->block_doh)for(size_t i=0;i<f->count;i++){vr_ip ip;if(!vr_ip_parse(f->entries[i],&ip))add_ip(ips,count,f->entries[i]);}
}
static int has_domains(const vr_dns_filter *f)
{if(f->block_doh)for(size_t i=0;i<f->count;i++){vr_ip ip;if(vr_ip_parse(f->entries[i],&ip))return 1;}return 0;}
static void *resolve_filter(void *arg)
{
    vr_engine *e=arg;struct vr_dns_filter_state *s=e->dns_filter;unsigned epoch=0;uint64_t next=0;
    for(;;) {
        pthread_mutex_lock(&e->mutex);int stop=s->stop;unsigned current=s->epoch;vr_config c=e->config;c.dns_filter=s->wanted;vr_addresses addresses=e->addresses;pthread_mutex_unlock(&e->mutex);
        if(stop)break;
        if(has_domains(&c.dns_filter)&&(current!=epoch||vr_millis()>=next)) {
            char ips[FILTER_IPS][46];size_t count=0;int failed=0,canceled=0;literal_ips(&c.dns_filter,ips,&count);
            for(size_t i=0;i<c.dns_filter.count;i++) {
                pthread_mutex_lock(&e->mutex);canceled=s->stop||s->epoch!=current;pthread_mutex_unlock(&e->mutex);if(canceled)break;
                vr_ip ip;if(!vr_ip_parse(c.dns_filter.entries[i],&ip))continue;
                char resolved[4][16];int n=vr_resolve_config4_all(c.dns_filter.entries[i],&c,addresses.wan_if,resolved);
                if(n<1)failed=1;
                for(int j=0;j<n&&j<4;j++)add_ip(ips,&count,resolved[j]);
            }
            pthread_mutex_lock(&e->mutex);
            if(!canceled&&!s->stop&&s->epoch==current) {
                /* A transient resolver failure must not remove known blocked IPs. */
                if(failed)for(size_t i=0;i<s->count;i++)add_ip(ips,&count,s->ips[i]);
                if(count!=s->count||memcmp(ips,s->ips,count*sizeof(ips[0]))) {memcpy(s->ips,ips,count*sizeof(ips[0]));s->count=count;s->serial++;}
            }
            pthread_mutex_unlock(&e->mutex);epoch=current;next=vr_millis()+(failed?60000:300000);
        }
        poll(NULL,0,100);
    }
    return NULL;
}
void vr_dns_filter_tick(vr_engine *e)
{
    vr_dns_filter wanted;pthread_mutex_lock(&e->service->mutex);wanted=e->service->config.dns_filter;pthread_mutex_unlock(&e->service->mutex);
    struct vr_dns_filter_state *s=e->dns_filter;
    if(!s) {
        if(!wanted.block_doh&&!wanted.block_dot&&!wanted.redirect_plain)return;
        s=calloc(1,sizeof(*s));if(!s)return;e->dns_filter=s;
    }
    pthread_mutex_lock(&e->mutex);
    if(memcmp(&wanted,&s->wanted,sizeof(wanted))) {s->wanted=wanted;s->epoch++;literal_ips(&wanted,s->ips,&s->count);s->serial++;}
    unsigned serial=s->serial;char ips[FILTER_IPS][46];size_t count=s->count;memcpy(ips,s->ips,count*sizeof(ips[0]));
    pthread_mutex_unlock(&e->mutex);
    vr_addresses a=vr_engine_addresses(e);uint64_t now=vr_millis();
    if(now>=s->retry&&(serial!=s->applied||memcmp(&s->addresses,&a,sizeof(a))||now>=s->check)) {
        /* Restore only owned chains, including after a vendor firewall reload. */
        if(vr_platform_dns_filter(&wanted,&a,ips,count)) {vr_service_log(e->service,"dns","LAN DNS controls could not be applied; retrying");s->retry=now+5000;return;}
        pthread_mutex_lock(&e->mutex);e->config.dns_filter=wanted;pthread_mutex_unlock(&e->mutex);
        s->applied=serial;s->addresses=a;s->check=now+30000;
    }
    if(!s->started&&has_domains(&wanted)) {
        pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,128*1024);
        int rc=pthread_create(&s->thread,&attr,resolve_filter,e);pthread_attr_destroy(&attr);
        if(!rc)s->started=1;
    }
}
void vr_dns_filter_stop(vr_engine *e)
{
    struct vr_dns_filter_state *s=e->dns_filter;if(!s)return;
    pthread_mutex_lock(&e->mutex);s->stop=1;pthread_mutex_unlock(&e->mutex);
    if(s->started)pthread_join(s->thread,NULL);
    vr_platform_dns_filter_stop();free(s);e->dns_filter=NULL;
}
