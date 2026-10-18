#define _GNU_SOURCE
#include "transport.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <curl/curl.h>

int __wrap_vr_resolve_config4_all(const char *host,const vr_config *config,const char *iface,char out[4][16])
{(void)host;(void)config;(void)iface;strcpy(out[0],"127.0.0.1");return 1;}
int main(int argc,char **argv)
{
 assert(argc==2);curl_global_init(CURL_GLOBAL_DEFAULT);vr_config c;vr_config_default(&c);
 snprintf(c.controller,sizeof(c.controller),"https://controller.fixture:%s",argv[1]);strcpy(c.node_id,"7");strcpy(c.node_key,"private-fixture-key");
 char dir[]="/tmp/chvr-link.XXXXXX";assert(mkdtemp(dir));char config[256],cache[256];snprintf(config,sizeof(config),"%s/config.json",dir);snprintf(cache,sizeof(cache),"%s/controller-direct.json",dir);
 vr_controller_link_init(&c,config);vr_controller_link_refresh(&c,"");char origin[1024],pin[65],err[256];
 vr_controller_link_feedback(&c,0,0,3000);assert(vr_controller_link_target(&c,origin,pin)==1);assert(!strcmp(origin,"https://192.0.2.1:15030")&&strlen(pin)==64);
 json_object *o=vr_controller_call(&c,"","GET","/api/probe/route/config",NULL,err,sizeof(err));if(!o)fprintf(stderr,"%s\n",err);assert(o&&json_object_get_boolean(vr_member(o,"ok")));json_object_put(o);
 vr_controller_link_init(&c,config);vr_controller_link_feedback(&c,0,0,3000);assert(vr_controller_link_target(&c,origin,pin)==1);
 char bytes[8192];size_t n;assert(!vr_read_file(cache,bytes,sizeof(bytes),&n));o=vr_json_parse(bytes,n);json_object *item=json_object_array_get_idx(vr_member(o,"endpoints"),0);char wrong[65];memset(wrong,'0',64);wrong[64]=0;json_object_object_add(item,"tls_spki_sha256",json_object_new_string(wrong));assert(!vr_controller_link_store(&c,o));json_object_put(o);
 vr_controller_link_feedback(&c,0,0,3000);o=vr_controller_call(&c,"","GET","/api/probe/route/config",NULL,err,sizeof(err));assert(!o);
 strcpy(c.controller,"https://other.fixture");vr_controller_link_init(&c,config);vr_controller_link_feedback(&c,0,0,3000);assert(vr_controller_link_target(&c,origin,pin)==0);
 unlink(cache);rmdir(dir);curl_global_cleanup();puts("PASS: authenticated discovery, pinned self-signed HTTP fallback, wrong pin rejection, restart cache and origin isolation");return 0;
}
