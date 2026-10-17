#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <curl/curl.h>

int vr_service_stopped(vr_service *s,unsigned generation)
{return s->stop||s->generation!=generation;}

/* The first resolved origin is unavailable; both HTTP authentication/config
   and the raw TLS control stream must reach the second verified address. */
int __wrap_vr_resolve_config4_all(const char *host,const vr_config *config,const char *iface,char out[4][16])
{(void)config;(void)iface;assert(!strcmp(host,"127.0.0.1"));strcpy(out[0],"127.0.0.2");strcpy(out[1],"127.0.0.1");return 2;}

int main(int argc,char **argv)
{
    assert(argc==2);signal(SIGPIPE,SIG_IGN);assert(!curl_global_init(CURL_GLOBAL_DEFAULT));
    vr_engine *e=calloc(1,sizeof(*e));assert(e);vr_service s={0};e->service=&s;
    pthread_mutex_init(&e->mutex,NULL);pthread_mutex_init(&s.mutex,NULL);vr_config_default(&e->config);
    assert(!vr_copy(e->config.controller,sizeof(e->config.controller),argv[1]));
    strcpy(e->config.node_id,"7");strcpy(e->config.node_key,"fixture-only-key");strcpy(e->addresses.lan,"127.0.0.2");
    const char *snapshot="{\"revision\":1}";e->snapshot=vr_json_parse(snapshot,strlen(snapshot));assert(e->snapshot);
    assert(!vr_control_start(e));int changed=0;uint64_t deadline=vr_millis()+15000;
    while(vr_millis()<deadline){pthread_mutex_lock(&e->mutex);changed=e->refresh;pthread_mutex_unlock(&e->mutex);if(changed)break;poll(NULL,0,20);}
    vr_control_stop(e);assert(changed);
    assert(s.pending_policy&&s.pending_generation==e->generation&&json_object_get_int(vr_member(s.pending_policy,"revision"))==2);
    json_object_put(s.pending_policy);pthread_mutex_destroy(&s.mutex);
    json_object_put(e->snapshot);pthread_mutex_destroy(&e->mutex);free(e);curl_global_cleanup();
    puts("PASS: real TLS/WebSocket/yamux controller authentication, report, command and refresh");return 0;
}
