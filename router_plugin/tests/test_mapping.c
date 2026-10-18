#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

static time_t wall=1792200000;
static uint64_t tick=100000;
static int calls,fail,writes;static time_t lifetime=1000000;
time_t __wrap_time(time_t *p){if(p)*p=wall;return wall;}
uint64_t __wrap_vr_millis(void){return tick;}
int __real_vr_atomic_write(const char *,const void *,size_t);
int __wrap_vr_atomic_write(const char *path,const void *bytes,size_t n)
{struct stat old;assert(n<VR_MAPPING_FILE_MAX);if(!stat(path,&old))assert(old.st_size+(off_t)n<1024*1024);writes++;return __real_vr_atomic_write(path,bytes,n);}
json_object *__wrap_vr_controller_call(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,char *err,size_t cap)
{
    (void)c;(void)iface;assert(!strcmp(method,"POST")&&!strcmp(path,"/api/probe/route/fake_ip/resolve"));
    __sync_fetch_and_add(&calls,1);usleep(50000);
    if(fail){snprintf(err,cap,"fixture timeout");return NULL;}
    json_object *r=json_object_new_object(),*item=json_object_new_object();
    const char *domain=vr_string(body,"domain");
    json_object_object_add(item,"domain",json_object_new_string(*domain?domain:"example.com"));
    json_object_object_add(item,"fake_ip",json_object_new_string("198.18.7.31"));
    json_object_object_add(item,"rule_id",json_object_new_string("proxy"));
    json_object_object_add(item,"exit_node_id",json_object_new_string("24"));
    json_object_object_add(item,"action",json_object_new_string("probe_exit"));
    time_t until=wall+lifetime;struct tm tm;gmtime_r(&until,&tm);char expires[32];assert(strftime(expires,sizeof(expires),"%Y-%m-%dT%H:%M:%SZ",&tm));
    json_object_object_add(item,"expires_at",json_object_new_string(expires));json_object_object_add(r,"item",item);return r;
}
static vr_decision decision(void){vr_decision d={VR_PROXY,"proxy","24","",0};return d;}
static vr_engine *engine(const char *file)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);pthread_mutex_init(&e->mutex,NULL);
    strcpy(e->config.controller,"https://fixture.invalid");strcpy(e->config.node_id,"25");strcpy(e->config.node_key,"fixture-key");
    strcpy(e->cidr,"198.18.0.0/15");strcpy(e->mapping_path,file);
    const char *p="{\"route_rules\":[{\"id\":\"proxy\",\"action\":\"probe_exit\",\"exit_node_id\":\"24\",\"entries\":[\"domain_suffix:example.com\"]}]}";
    e->policy=vr_json_parse(p,strlen(p));assert(e->policy);vr_mapping_load(e);return e;
}
static void drop(vr_engine *e){json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);}
static void *request(void *arg){vr_mapping m;assert(!vr_mapping_get(arg,"example.com",NULL,decision(),&m));return NULL;}
static int count(vr_engine *e){int n=0;for(size_t i=0;i<VR_MAPPING_MAX;i++)if(e->mappings[i].expires>wall)n++;return n;}
int main(void)
{
    char dir[]="/tmp/chvr-mapping-XXXXXX",path[512];assert(mkdtemp(dir));snprintf(path,sizeof(path),"%s/fake-ip-cache.json",dir);
    vr_engine *e=engine(path);pthread_t workers[5];
    for(size_t i=0;i<5;i++)assert(!pthread_create(&workers[i],NULL,request,e));
    for(size_t i=0;i<5;i++)pthread_join(workers[i],NULL);
    assert(calls==1);vr_mapping m;assert(!vr_mapping_get(e,"example.com",NULL,decision(),&m));assert(m.expires==wall+VR_MAPPING_TTL);
    wall+=31;tick+=31000;assert(!vr_mapping_get(e,"example.com",NULL,decision(),&m)&&calls==1);
    e->mappings[0].resolved=1234;assert(!vr_mapping_flush(e,0)&&writes==1);
    struct stat st;assert(!stat(path,&st)&&st.st_size<VR_MAPPING_FILE_MAX&&(st.st_mode&0777)==0600);
    vr_engine *restored=engine(path);assert(count(restored)==1);assert(!vr_mapping_get(restored,"example.com",NULL,decision(),&m));
    assert(!strcmp(m.exit,"24")&&!strcmp(m.rule,"proxy")&&!m.resolved&&calls==1);drop(restored);
    char interrupted[540];snprintf(interrupted,sizeof(interrupted),"%s.aBc123",path);
    FILE *orphan=fopen(interrupted,"w");assert(orphan);fputs("interrupted",orphan);fclose(orphan);
    restored=engine(path);assert(access(interrupted,F_OK)&&count(restored)==1);drop(restored);
    /* A restart never extends the original expiration. */
    wall+=600;restored=engine(path);assert(!vr_mapping_get(restored,NULL,"198.18.7.31",decision(),&m)&&calls==1);assert(m.expires==wall-631+VR_MAPPING_TTL);drop(restored);
    /* Shorter controller TTL wins, including after persistence. */
    e->mappings[0].expires=wall;lifetime=17;
    assert(!vr_mapping_get(e,"example.com",NULL,decision(),&m)&&calls==2&&m.expires==wall+17);
    assert(!vr_mapping_flush(e,0)&&writes==1);assert(!vr_mapping_flush(e,1)&&writes==2);
    wall+=18;restored=engine(path);assert(!count(restored));drop(restored);
    fail=1;assert(vr_mapping_get(e,"example.com",NULL,decision(),&m)&&calls==3);
    for(int i=0;i<10;i++)assert(vr_mapping_get(e,"example.com",NULL,decision(),&m));
    assert(calls==3);
    tick+=1001;fail=0;lifetime=3600;assert(!vr_mapping_get(e,"example.com",NULL,decision(),&m)&&calls==4);
    assert(!vr_mapping_flush(e,1));
    /* The cache is tied to identity, secret, CIDR and the route rule snapshot. */
    for(int variant=0;variant<5;variant++) {
        restored=engine(path);memset(restored->mappings,0,sizeof(restored->mappings));
        if(variant==0)strcpy(restored->config.node_id,"26");
        if(variant==1)strcpy(restored->config.controller,"https://other.invalid");
        if(variant==2)strcpy(restored->config.node_key,"other-key");
        if(variant==3)strcpy(restored->cidr,"198.19.0.0/16");
        if(variant==4)json_object_object_add(json_object_array_get_idx(vr_member(restored->policy,"route_rules"),0),"action",json_object_new_string("direct"));
        vr_mapping_load(restored);assert(!count(restored));drop(restored);
    }
    wall-=100;restored=engine(path);assert(!count(restored));drop(restored);wall+=100;
    /* Worst-case escaped fields must fit both generations under 1 MiB. */
    for(size_t i=0;i<VR_MAPPING_MAX;i++) {
        vr_mapping *v=&e->mappings[i];memset(v,0,sizeof(*v));
        memset(v->domain,'"',sizeof(v->domain)-1);snprintf(v->domain,16,"%zu.test",i);v->domain[strlen(v->domain)]='"';v->domain[253]=0;
        memset(v->rule,'"',128);memset(v->exit,'"',128);snprintf(v->ip,sizeof(v->ip),"198.19.%zu.%zu",i/256,i%256);
        v->expires=wall+3600;v->used=i;
    }
    e->mapping_dirty++;assert(!vr_mapping_flush(e,1));assert(!stat(path,&st)&&2*st.st_size<1024*1024);
    restored=engine(path);assert(count(restored)>0&&count(restored)<VR_MAPPING_MAX);drop(restored);
    assert(!vr_mapping_flush(e,0));int saved_writes=writes;
    e->mapping_dirty++;assert(!vr_mapping_flush(e,0)&&writes==saved_writes);tick+=60000;assert(!vr_mapping_flush(e,0)&&writes==saved_writes+1);
    FILE *f=fopen(path,"w");assert(f);fputs("{broken",f);fclose(f);restored=engine(path);assert(!count(restored));drop(restored);
    f=fopen(path,"w");assert(f);assert(!ftruncate(fileno(f),VR_MAPPING_FILE_MAX+1));fclose(f);restored=engine(path);assert(!count(restored));drop(restored);
    assert(access(path,F_OK));
    drop(e);unlink(path);rmdir(dir);
    puts("PASS: TTL, singleflight, retry cooldown, bounded atomic JFFS cache, restart and identity/policy invalidation");return 0;
}
