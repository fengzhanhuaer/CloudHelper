#include "chvr.h"
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

json_object *vr_cache_load(const char *path)
{
    char *b=malloc(VR_JSON_MAX);if(!b)return NULL;size_t n;
    json_object *o=vr_read_file(path,b,VR_JSON_MAX,&n)?NULL:vr_json_parse(b,n);free(b);return o;
}
int vr_cache_save(const char *path,json_object *o)
{
    const char *text=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);size_t n=strlen(text),oldn;
    char *old=malloc(VR_JSON_MAX);if(!old)return -1;
    int same=!vr_read_file(path,old,VR_JSON_MAX,&oldn)&&n==oldn&&!memcmp(old,text,n);free(old);
    return same?0:vr_atomic_write(path,text,n);
}
json_object *vr_policy_fetch_cached(const vr_config *c,const char *iface,const char *path,char *err,size_t n)
{
    json_object *o=vr_controller_call(c,iface,"GET","/api/probe/route/config",NULL,err,n);
    int online=o!=NULL;
    if(!online)o=vr_cache_load(path);
    if(!o||vr_policy_validate(o,c->node_id,err,n)){json_object_put(o);return NULL;}
    if(online&&vr_cache_save(path,o)){snprintf(err,n,"cannot persist route configuration");json_object_put(o);return NULL;}
    if(n)*err=0;
    return o;
}
