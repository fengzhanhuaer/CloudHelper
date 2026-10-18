#define _GNU_SOURCE
#include "transport.h"
#include <arpa/inet.h>
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <openssl/x509_vfy.h>

static struct {
    pthread_mutex_t mutex;
    char controller[1024],path[512];
    json_object *data;
    uint64_t prefer_until,last_refresh;
    size_t index;
    int refreshing,healthy;
} link_state={.mutex=PTHREAD_MUTEX_INITIALIZER};

static int valid(json_object *o)
{
    if(!o)return 0;
    int64_t expiry=json_object_get_int64(vr_member(o,"expires_at"));
    json_object *items=vr_member(o,"endpoints");if(!json_object_is_type(items,json_type_array))return 0;size_t count=json_object_array_length(items);
    if(json_object_get_int(vr_member(o,"version"))!=1||expiry<=time(NULL)||expiry>time(NULL)+31*86400||!count||count>4)return 0;
    for(size_t i=0;i<count;i++) {
        json_object *item=json_object_array_get_idx(items,i);struct in_addr ip;
        const char *pin=vr_string(item,"tls_spki_sha256");
        if(inet_pton(AF_INET,vr_string(item,"ip"),&ip)!=1||json_object_get_int(vr_member(item,"port"))!=15030||strlen(pin)!=64)return 0;
        uint32_t host=ntohl(ip.s_addr);if(!host||(host>>24)==127||(host>>28)>=14)return 0;
        for(size_t j=0;j<64;j++)if(!((pin[j]>='0'&&pin[j]<='9')||(pin[j]>='a'&&pin[j]<='f')))return 0;
    }
    return 1;
}
void vr_controller_link_init(const vr_config *c,const char *config_path)
{
    pthread_mutex_lock(&link_state.mutex);
    json_object_put(link_state.data);link_state.data=NULL;link_state.index=0;link_state.prefer_until=0;link_state.last_refresh=0;link_state.refreshing=0;link_state.healthy=0;
    snprintf(link_state.controller,sizeof(link_state.controller),"%s",c->controller);
    snprintf(link_state.path,sizeof(link_state.path),"%s",config_path);
    char *slash=strrchr(link_state.path,'/');if(slash)slash[1]=0;else *link_state.path=0;
    if(strlen(link_state.path)+sizeof("controller-direct.json")<sizeof(link_state.path))strcat(link_state.path,"controller-direct.json");
    char bytes[8192];size_t n;
    if(!vr_read_file(link_state.path,bytes,sizeof(bytes),&n)) {
        json_object *o=vr_json_parse(bytes,n);
        if(!strcmp(vr_string(o,"controller"),c->controller)&&valid(o)){json_object_put(link_state.data);link_state.data=json_object_get(o);}
        json_object_put(o);
    }
    pthread_mutex_unlock(&link_state.mutex);
}
int vr_controller_link_target(const vr_config *c,char origin[1024],char pin[65])
{
    snprintf(origin,1024,"%s",c->controller);*pin=0;int direct=0;
    pthread_mutex_lock(&link_state.mutex);
    if(!strcmp(link_state.controller,c->controller)&&valid(link_state.data)&&vr_millis()<link_state.prefer_until) {
        json_object *items=vr_member(link_state.data,"endpoints");
        json_object *item=json_object_array_get_idx(items,link_state.index%json_object_array_length(items));
        snprintf(origin,1024,"https://%s:15030",vr_string(item,"ip"));snprintf(pin,65,"%s",vr_string(item,"tls_spki_sha256"));direct=1;
    }
    pthread_mutex_unlock(&link_state.mutex);return direct;
}
void vr_controller_link_feedback(const vr_config *c,int direct,int ok,uint64_t elapsed)
{
    pthread_mutex_lock(&link_state.mutex);
    if(!strcmp(c->controller,link_state.controller)) {
        if(!direct&&(!ok||elapsed>2500)){link_state.prefer_until=vr_millis()+60000;link_state.healthy=0;}
        else if(!direct&&ok&&elapsed<2000&&++link_state.healthy>=2)link_state.prefer_until=0;
        else if(direct&&!ok) {
            link_state.index++;
            if(link_state.index>=(size_t)json_object_array_length(vr_member(link_state.data,"endpoints"))){link_state.index=0;link_state.prefer_until=0;}
        }
    }
    pthread_mutex_unlock(&link_state.mutex);
}
int vr_controller_link_refresh_due(const vr_config *c)
{
    pthread_mutex_lock(&link_state.mutex);int due=0;
    if(!strcmp(c->controller,link_state.controller)&&!link_state.refreshing&&vr_millis()>=link_state.last_refresh) {
        link_state.refreshing=1;link_state.last_refresh=vr_millis()+30000;due=1;
    }
    pthread_mutex_unlock(&link_state.mutex);return due;
}
void vr_controller_link_refresh_done(void)
{pthread_mutex_lock(&link_state.mutex);link_state.refreshing=0;pthread_mutex_unlock(&link_state.mutex);}
int vr_controller_link_store(const vr_config *c,json_object *data)
{
    json_object *items=vr_member(data,"endpoints");if(!json_object_is_type(items,json_type_array)||(size_t)json_object_array_length(items)>4)return -1;
    json_object *filtered=json_object_new_object(),*v4=json_object_new_array();
    json_object_object_add(filtered,"version",json_object_get(vr_member(data,"version")));
    json_object_object_add(filtered,"expires_at",json_object_get(vr_member(data,"expires_at")));
    for(size_t i=0;i<(size_t)json_object_array_length(items);i++){json_object *item=json_object_array_get_idx(items,i);struct in_addr ip;if(inet_pton(AF_INET,vr_string(item,"ip"),&ip)==1)json_object_array_add(v4,json_object_get(item));}
    json_object_object_add(filtered,"endpoints",v4);data=filtered;
    if(!valid(data)){json_object_put(filtered);return -1;}
    pthread_mutex_lock(&link_state.mutex);
    if(strcmp(c->controller,link_state.controller)){pthread_mutex_unlock(&link_state.mutex);json_object_put(filtered);return -1;}
    json_object_object_add(data,"controller",json_object_new_string(c->controller));
    const char *bytes=json_object_to_json_string_ext(data,JSON_C_TO_STRING_PLAIN);
    int rc=strlen(bytes)<8192?vr_atomic_write(link_state.path,bytes,strlen(bytes)):-1;
    if(!rc){json_object_put(link_state.data);link_state.data=json_object_get(data);link_state.last_refresh=vr_millis()+3600000;}
    pthread_mutex_unlock(&link_state.mutex);json_object_put(filtered);return rc;
}
static int verify_pinned(X509_STORE_CTX *store,void *pin)
{return vr_tls_pinned(X509_STORE_CTX_get0_cert(store),pin)==0;}
static CURLcode ssl_context(CURL *curl,void *context,void *pin)
{(void)curl;SSL_CTX_set_cert_verify_callback(context,verify_pinned,pin);return CURLE_OK;}
void vr_curl_pin(CURL *curl,const char *pin)
{
    if(!*pin)return;
    curl_easy_setopt(curl,CURLOPT_SSL_VERIFYHOST,0L);
    curl_easy_setopt(curl,CURLOPT_SSL_VERIFYPEER,1L);
    curl_easy_setopt(curl,CURLOPT_SSL_CTX_FUNCTION,ssl_context);
    curl_easy_setopt(curl,CURLOPT_SSL_CTX_DATA,pin);
}
