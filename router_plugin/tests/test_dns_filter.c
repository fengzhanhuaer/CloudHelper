#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include "../src/dns_filter.c"

static int applied,stopped,fail;
static vr_dns_filter installed;
static char installed_ips[FILTER_IPS][46];static size_t installed_count;
int vr_platform_dns_filter(const vr_dns_filter *f,const vr_addresses *a,const char ips[][46],size_t count)
{assert(!strcmp(a->lan_if,"lan0"));if(fail)return -1;installed=*f;installed_count=count;memcpy(installed_ips,ips,count*sizeof(ips[0]));applied++;return 0;}
void vr_platform_dns_filter_stop(void){stopped++;}
int __wrap_vr_resolve_config4_all(const char *host,const vr_config *c,const char *iface,char out[4][16])
{assert(!strcmp(host,"dns.example.test")&&!strcmp(iface,"wan0")&&c->dns_enabled);usleep(50000);strcpy(out[0],"203.0.113.53");return 1;}
static int has_ip(const char *ip){for(size_t i=0;i<installed_count;i++)if(!strcmp(installed_ips[i],ip))return 1;return 0;}
int main(void)
{
    vr_service service={0};pthread_mutex_init(&service.mutex,NULL);vr_config_default(&service.config);
    vr_engine *e=calloc(1,sizeof(*e));assert(e);e->service=&service;e->config=service.config;pthread_mutex_init(&e->mutex,NULL);
    strcpy(e->addresses.lan,"192.0.2.1");strcpy(e->addresses.lan_if,"lan0");strcpy(e->addresses.wan_if,"wan0");
    vr_dns_filter_tick(e);assert(!e->dns_filter&&!applied);
    vr_dns_filter *f=&service.config.dns_filter;f->block_doh=f->block_dot=f->redirect_plain=1;f->count=2;strcpy(f->entries[0],"1.1.1.1");strcpy(f->entries[1],"dns.example.test");
    strcpy(e->mappings[0].domain,"dns.example.test");strcpy(e->mappings[0].ip,"198.18.0.20");e->mappings[0].expires=time(NULL)+600;
    uint64_t before=vr_millis();vr_dns_filter_tick(e);assert(vr_millis()-before<200&&applied==1&&has_ip("1.1.1.1")&&e->mappings[0].dns_blocked);
    for(int i=0;i<100&&!has_ip("203.0.113.53");i++){usleep(10000);vr_dns_filter_tick(e);}
    assert(has_ip("203.0.113.53")&&has_ip("1.1.1.1")&&installed.block_dot&&installed.redirect_plain);
    int old=applied;for(int i=0;i<10;i++)vr_dns_filter_tick(e);assert(applied==old); /* no repeated subprocess batch */
    f->block_doh=f->block_dot=f->redirect_plain=0;vr_dns_filter_tick(e);assert(!installed.block_doh&&!installed.block_dot&&!installed.redirect_plain&&!installed_count&&!e->mappings[0].dns_blocked);
    fail=1;f->block_dot=1;vr_dns_filter_tick(e);assert(!e->config.dns_filter.block_dot);
    fail=0;e->dns_filter->retry=0;vr_dns_filter_tick(e);assert(e->config.dns_filter.block_dot&&installed.block_dot);
    vr_dns_filter_stop(e);assert(stopped==1&&!e->dns_filter);
    pthread_mutex_destroy(&e->mutex);pthread_mutex_destroy(&service.mutex);free(e);
    puts("DNS controls: independent switches, literals, background domain resolution, disable and failed-apply retry PASS");return 0;
}
