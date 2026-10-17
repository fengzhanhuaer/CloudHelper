#define _GNU_SOURCE
#include "transport.h"
#include <assert.h>
#include <curl/curl.h>
#include <stdio.h>
#include <string.h>

int __wrap_vr_resolve_config4_all(const char *host,const vr_config *config,const char *iface,char out[4][16])
{(void)config;(void)iface;assert(!strcmp(host,"127.0.0.1"));strcpy(out[0],"127.0.0.2");strcpy(out[1],"127.0.0.1");return 2;}
static int cancelled(void *arg){return vr_millis()>=*(uint64_t *)arg;}
int main(int argc,char **argv)
{
    assert(argc==2);assert(!curl_global_init(CURL_GLOBAL_DEFAULT));
    vr_config c;vr_config_default(&c);assert(!vr_copy(c.controller,sizeof(c.controller),argv[1]));
    strcpy(c.node_id,"7");strcpy(c.node_key,"fixture-only-key");char err[256];
    uint64_t start=vr_millis();json_object *cert=vr_controller_certificate(&c,"",0,NULL,NULL,err,sizeof(err));
    assert(cert&&!strcmp(vr_string(cert,"node_id"),"7")&&!*err);json_object_put(cert);
    assert(vr_millis()-start>=11500&&vr_millis()-start<18000);
    start=vr_millis();cert=vr_controller_certificate(&c,"",1,NULL,NULL,err,sizeof(err));
    assert(!cert&&!strcmp(err,"controller transport failed (28)"));assert(vr_millis()-start>=9500&&vr_millis()-start<14000);
    start=vr_millis();cert=vr_controller_call(&c,"","GET","/slow",NULL,err,sizeof(err));
    assert(!cert&&!strcmp(err,"controller transport failed (28)"));assert(vr_millis()-start>=9500&&vr_millis()-start<14000);
    uint64_t stop=vr_millis()+200;start=vr_millis();cert=vr_controller_certificate(&c,"",0,cancelled,&stop,err,sizeof(err));
    assert(!cert&&!strcmp(err,"controller request cancelled"));assert(vr_millis()-start<3000);
    stop=0;start=vr_millis();assert(!vr_controller_certificate(&c,"",0,cancelled,&stop,err,sizeof(err)));
    assert(!strcmp(err,"controller request cancelled")&&vr_millis()-start<100);
    assert(!vr_controller_call(&c,"","GET","/fail",NULL,err,sizeof(err))&&!strcmp(err,"controller HTTP 503"));
    curl_global_cleanup();puts("PASS: TLS/HMAC first certificate beyond 10s, short cached refresh and ordinary HTTP, cancellation and exact HTTP error");return 0;
}
