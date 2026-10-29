#define _GNU_SOURCE
#include "transport.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <curl/curl.h>
#include "../src/upgrade.c"

int vr_service_stopped(vr_service *s,unsigned generation)
{return s->stop||s->generation!=generation;}

int __wrap_vr_resolve_transport(const char *host,const vr_config *config,const char *iface,char out[4][46])
{(void)host;(void)config;(void)iface;strcpy(out[0],"127.0.0.1");return 1;}
int main(int argc,char **argv)
{
 assert(argc==2);curl_global_init(CURL_GLOBAL_DEFAULT);vr_config c;vr_config_default(&c);
 snprintf(c.controller,sizeof(c.controller),"https://controller.fixture:%s",argv[1]);strcpy(c.node_id,"7");strcpy(c.node_key,"private-fixture-key");
 char dir[]="/tmp/chvr-link.XXXXXX";assert(mkdtemp(dir));char config[256],cache[256];snprintf(config,sizeof(config),"%s/config.json",dir);snprintf(cache,sizeof(cache),"%s/controller-direct.json",dir);
 vr_controller_link_init(&c,config);uint64_t started=vr_millis();vr_controller_link_refresh(&c,"");assert(vr_millis()-started<200);char origin[1024],pin[65],err[256];
 for(int i=0;i<150&&access(cache,F_OK);i++)usleep(100000);
 assert(!access(cache,F_OK));
 assert(!vr_controller_link_target(&c,origin,pin));
 assert(vr_controller_link_download_target(&c,origin,pin)==1);
 assert(!strcmp(origin,"https://192.0.2.1:15030")&&strlen(pin)==64);
 /* Keep one origin for bounded download-fallback tests after verifying v4
    preference over the discovered v6 endpoint above. */
 char saved[8192];size_t saved_n;assert(!vr_read_file(cache,saved,sizeof(saved),&saved_n));
 json_object *discovery=vr_json_parse(saved,saved_n),*endpoints=json_object_new_array();
 json_object_array_add(endpoints,json_object_get(json_object_array_get_idx(vr_member(discovery,"endpoints"),0)));
 json_object_object_add(discovery,"endpoints",endpoints);assert(!vr_controller_link_store(&c,discovery));json_object_put(discovery);
 upgrade_job job={.config=c};transfer metadata={.job=&job,.fd=-1};
 assert(fetch(&job,"https://fixture/metadata",&metadata)==CURLE_OK&&metadata.status==200);
 json_object *download=vr_json_parse(metadata.memory,metadata.used);assert(download&&!strcmp(vr_string(download,"download"),"ip"));json_object_put(download);free(metadata.memory);
 assert(!vr_controller_link_target(&c,origin,pin));
 FILE *file=tmpfile();assert(file&&write(fileno(file),"ab",2)==2);
 transfer body={.job=&job,.fd=fileno(file),.offset=2,.size=8};
 assert(fetch(&job,"https://fixture/fail",&body)==CURLE_OK&&body.status==206&&body.received==6);
 char result[9]={0};rewind(file);assert(fread(result,1,8,file)==8&&!strcmp(result,"abcdefgh"));fclose(file);
 assert(!vr_controller_link_download_target(&c,origin,pin));
 vr_controller_link_init(&c,config);
 vr_controller_link_feedback(&c,0,0,3000);assert(vr_controller_link_target(&c,origin,pin)==1);assert(!strcmp(origin,"https://192.0.2.1:15030")&&strlen(pin)==64);
 json_object *o=vr_controller_call(&c,"","GET","/api/probe/route/config",NULL,err,sizeof(err));if(!o)fprintf(stderr,"%s\n",err);assert(o&&json_object_get_boolean(vr_member(o,"ok")));json_object_put(o);
 vr_controller_link_init(&c,config);vr_controller_link_feedback(&c,0,0,3000);assert(vr_controller_link_target(&c,origin,pin)==1);
 char bytes[8192];size_t n;assert(!vr_read_file(cache,bytes,sizeof(bytes),&n));o=vr_json_parse(bytes,n);json_object *item=json_object_array_get_idx(vr_member(o,"endpoints"),0);char wrong[65];memset(wrong,'0',64);wrong[64]=0;json_object_object_add(item,"tls_spki_sha256",json_object_new_string(wrong));assert(!vr_controller_link_store(&c,o));json_object_put(o);
 vr_controller_link_feedback(&c,0,0,3000);o=vr_controller_call(&c,"","GET","/api/probe/route/config",NULL,err,sizeof(err));assert(!o);
 strcpy(c.controller,"https://other.fixture");vr_controller_link_init(&c,config);vr_controller_link_feedback(&c,0,0,3000);assert(vr_controller_link_target(&c,origin,pin)==0);
 unlink(cache);rmdir(dir);curl_global_cleanup();puts("PASS: discovery, download IP priority, fresh-auth range fallback, wrong pin rejection, restart cache and origin isolation");return 0;
}
