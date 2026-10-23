#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <curl/curl.h>
#include <unistd.h>

static const char *fixture_origin;static unsigned public_requests,public_resolves;
CURLcode __real_curl_easy_perform(CURL *curl);
CURLcode __wrap_curl_easy_perform(CURL *curl)
{
    char *url=NULL;curl_easy_getinfo(curl,CURLINFO_EFFECTIVE_URL,&url);
    if(url&&(!strcmp(url,"https://api4.ipify.org")||!strcmp(url,"https://ipv4.icanhazip.com"))) {
        if(++public_requests==1)return CURLE_OPERATION_TIMEDOUT;
        char target[256];snprintf(target,sizeof(target),"%s/fixture-public-ip",fixture_origin);curl_easy_setopt(curl,CURLOPT_URL,target);
    }
    return __real_curl_easy_perform(curl);
}

int vr_service_stopped(vr_service *s,unsigned generation)
{return s->stop||s->generation!=generation;}

/* The first resolved origin is unavailable; both HTTP authentication/config
   and the raw TLS control stream must reach the second verified address. */
int __wrap_vr_resolve_config4_all(const char *host,const vr_config *config,const char *iface,char out[4][16])
{(void)config;(void)iface;
 if(!strcmp(host,"api4.ipify.org")||!strcmp(host,"ipv4.icanhazip.com")){public_resolves++;strcpy(out[0],"203.0.113.1");return 1;}
 assert(!strcmp(host,"127.0.0.1"));strcpy(out[0],"127.0.0.2");strcpy(out[1],"127.0.0.1");return 2;}

int main(int argc,char **argv)
{
    assert(argc==3);fixture_origin=argv[1];signal(SIGPIPE,SIG_IGN);assert(!curl_global_init(CURL_GLOBAL_DEFAULT));
    vr_engine *e=calloc(1,sizeof(*e));assert(e);vr_service s={0};e->service=&s;
    pthread_mutex_init(&e->mutex,NULL);pthread_mutex_init(&s.mutex,NULL);vr_config_default(&e->config);
    char root[]="/tmp/chvr-control-XXXXXX";assert(mkdtemp(root));snprintf(s.config_path,sizeof(s.config_path),"%s/config.json",root);
    assert(!vr_copy(e->config.controller,sizeof(e->config.controller),argv[1]));
    strcpy(e->config.node_id,"7");strcpy(e->config.node_key,"fixture-only-key");strcpy(e->addresses.lan,"127.0.0.2");
    strcpy(e->addresses.wan,"10.0.0.1");strcpy(e->addresses.wan_if,"lo");
    const char *snapshot="{\"node_id\":\"7\",\"expected_node_kind\":\"asus_56uv2\",\"revision\":1,\"virtual_router\":{\"fake_ip_cidr\":\"198.18.0.0/15\",\"probe_ips\":[{\"node_id\":\"7\",\"ip\":\"198.18.0.7\"}],\"topology_rules\":[],\"route_rules\":[],\"updated_at\":\"old\"}}";e->snapshot=vr_json_parse(snapshot,strlen(snapshot));assert(e->snapshot);
    e->policy=vr_member(e->snapshot,"virtual_router");strcpy(e->own_ip,"198.18.0.7");strcpy(e->cidr,"198.18.0.0/15");
    char *fixture=malloc(256*1024);size_t fixture_len;assert(fixture&&!vr_read_file(argv[2],fixture,256*1024,&fixture_len));
    e->telemetry=vr_json_parse(fixture,fixture_len);free(fixture);assert(e->telemetry);
    assert(!vr_control_start(e));int changed=0;uint64_t deadline=vr_millis()+40000;
    while(vr_millis()<deadline){changed=vr_policy_apply_pending(e);if(changed)break;poll(NULL,0,20);}
    assert(changed==1&&!e->refresh&&!s.pending_policy&&json_object_get_int(vr_member(e->snapshot,"revision"))==2);
    deadline=vr_millis()+5000;while(vr_millis()<deadline){assert(vr_policy_apply_pending(e)!=2);pthread_mutex_lock(&e->mutex);int reset=e->buffers_reset;pthread_mutex_unlock(&e->mutex);if(reset)vr_telemetry_publish(e);poll(NULL,0,20);}
    pthread_mutex_lock(&e->mutex);assert(e->controller_connected);assert(!strcmp(e->controller_endpoint,argv[1]+8));assert(!e->controller_direct);pthread_mutex_unlock(&e->mutex);
    vr_control_stop(e);assert(!e->controller_connected&&!e->controller_endpoint[0]&&!e->controller_direct);pthread_mutex_destroy(&s.mutex);
    assert(public_requests==2&&public_resolves==2&&!strcmp(e->public_ipv4,"1.2.3.4"));
    char cache[512];snprintf(cache,sizeof(cache),"%s/route-cache.json",root);assert(!unlink(cache)&&!rmdir(root));
    assert(e->telemetry_epoch==1);json_object_put(e->telemetry);json_object_put(e->snapshot);pthread_mutex_destroy(&e->mutex);free(e);curl_global_cleanup();
    puts("PASS: real TLS/WebSocket/yamux config sync applies policy without closing control stream");return 0;
}
