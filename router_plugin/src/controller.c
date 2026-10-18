#include "transport.h"
#include <curl/curl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {char *bytes;size_t used,cap;} response;
typedef struct {int (*check)(void *);void *arg;} cancellation;
static int progress(void *arg,curl_off_t down_total,curl_off_t down,curl_off_t up_total,curl_off_t up)
{
    (void)down_total;(void)down;(void)up_total;(void)up;
    cancellation *c=arg;return c->check&&c->check(c->arg);
}

static size_t receive(void *data,size_t size,size_t count,void *arg)
{
    response *r=arg;
    if(size && count>VR_JSON_MAX/size)return 0;
    size_t n=size*count;
    if(n>VR_JSON_MAX-r->used)return 0;
    if(r->used+n+1>r->cap) {
        size_t cap=r->cap?r->cap:4096;
        while(cap<r->used+n+1)cap*=2;
        if(cap>VR_JSON_MAX+1)cap=VR_JSON_MAX+1;
        void *p=realloc(r->bytes,cap);if(!p)return 0;r->bytes=p;r->cap=cap;
    }
    memcpy(r->bytes+r->used,data,n);r->used+=n;r->bytes[r->used]=0;return n;
}

static json_object *request_at(const vr_config *c,const char *origin,const char *pin,const char *iface,const char *method,const char *path,json_object *body,struct curl_slist *headers,long budget,cancellation *cancel,char *err,size_t n)
{
    if(cancel&&cancel->check&&cancel->check(cancel->arg)){snprintf(err,n,"controller request cancelled");return NULL;}
    char url[2048],host[254],ips[4][16],resolve[320];int port,tls;
    if(vr_origin(origin,host,&port,&tls) || snprintf(url,sizeof(url),"%s%s",origin,path)>=(int)sizeof(url)) {snprintf(err,n,"invalid controller URL");return NULL;}
    /* Resolve outside dnsmasq to prevent recursion when DNS routing is enabled. */
    int count=vr_resolve_config4_all(host,c,iface,ips);if(count<1){snprintf(err,n,"controller DNS failed");return NULL;}
    struct curl_slist *addresses=NULL;
    CURL *curl=curl_easy_init();response r={0};json_object *result=NULL;
    if(!curl){snprintf(err,n,"controller allocation failed");goto done;}
    curl_easy_setopt(curl,CURLOPT_URL,url);
    curl_easy_setopt(curl,CURLOPT_PROXY,"");
    curl_easy_setopt(curl,CURLOPT_PROTOCOLS,(long)(CURLPROTO_HTTP|CURLPROTO_HTTPS));
    curl_easy_setopt(curl,CURLOPT_FOLLOWLOCATION,0L);
    curl_easy_setopt(curl,CURLOPT_CONNECTTIMEOUT,5L);curl_easy_setopt(curl,CURLOPT_TIMEOUT,10L);
    curl_easy_setopt(curl,CURLOPT_NOSIGNAL,1L);curl_easy_setopt(curl,CURLOPT_RESOLVE,addresses);
    if(cancel&&cancel->check) {
        curl_easy_setopt(curl,CURLOPT_NOPROGRESS,0L);
        curl_easy_setopt(curl,CURLOPT_XFERINFOFUNCTION,progress);curl_easy_setopt(curl,CURLOPT_XFERINFODATA,cancel);
    }
    curl_easy_setopt(curl,CURLOPT_SSL_VERIFYPEER,1L);curl_easy_setopt(curl,CURLOPT_SSL_VERIFYHOST,2L);
    curl_easy_setopt(curl,CURLOPT_SSLVERSION,(long)CURL_SSLVERSION_TLSv1_2);
    vr_curl_pin(curl,pin);
    curl_easy_setopt(curl,CURLOPT_HTTPHEADER,headers);
    if(iface && *iface)curl_easy_setopt(curl,CURLOPT_INTERFACE,iface);
    curl_easy_setopt(curl,CURLOPT_WRITEFUNCTION,receive);curl_easy_setopt(curl,CURLOPT_WRITEDATA,&r);
    curl_easy_setopt(curl,CURLOPT_CUSTOMREQUEST,method);
    if(body)curl_easy_setopt(curl,CURLOPT_POSTFIELDS,json_object_to_json_string_ext(body,JSON_C_TO_STRING_PLAIN));
    CURLcode rc=CURLE_OPERATION_TIMEDOUT;long status=0;uint64_t deadline=vr_millis()+(uint64_t)budget;
    for(int i=0;i<count;i++) {
        uint64_t now=vr_millis();if(now>=deadline)break;
        long remaining=(long)(deadline-now),connect_budget=remaining/(count-i);if(connect_budget>5000)connect_budget=5000;
        /* Older vendor libcurl retains a permanent RESOLVE entry. Explicitly
           remove it before replacing the address on this easy handle. */
        snprintf(resolve,sizeof(resolve),"-%s:%d",host,port);
        curl_slist_free_all(addresses);addresses=curl_slist_append(NULL,resolve);
        if(!addresses){snprintf(err,n,"controller allocation failed");goto done;}
        snprintf(resolve,sizeof(resolve),"%s:%d:%s",host,port,ips[i]);
        struct curl_slist *next=curl_slist_append(addresses,resolve);
        if(!next){snprintf(err,n,"controller allocation failed");goto done;}addresses=next;
        curl_easy_setopt(curl,CURLOPT_RESOLVE,addresses);curl_easy_setopt(curl,CURLOPT_FRESH_CONNECT,1L);
        curl_easy_setopt(curl,CURLOPT_CONNECTTIMEOUT_MS,connect_budget);curl_easy_setopt(curl,CURLOPT_TIMEOUT_MS,remaining);
        r.used=0;status=0;rc=curl_easy_perform(curl);curl_easy_getinfo(curl,CURLINFO_RESPONSE_CODE,&status);
        if(rc==CURLE_ABORTED_BY_CALLBACK)break;
        if(rc==CURLE_OK||status)break;
        double handshake=0;curl_easy_getinfo(curl,CURLINFO_APPCONNECT_TIME,&handshake);
        /* Retry GET safely; never replay a POST after TLS accepted its body. */
        if(strcmp(method,"GET")&&handshake>0)break;
    }
    if(rc!=CURLE_OK){if(rc==CURLE_ABORTED_BY_CALLBACK)snprintf(err,n,"controller request cancelled");else snprintf(err,n,"controller transport failed (%d)",(int)rc);goto done;}
    if(status!=200){snprintf(err,n,"controller HTTP %ld",status);goto done;}
    result=vr_json_parse(r.bytes,r.used);
    if(!result)snprintf(err,n,"invalid controller JSON");else if(n)*err=0;
done:
    if(r.bytes){memset(r.bytes,0,r.used);free(r.bytes);}
    if(curl)curl_easy_cleanup(curl);
    curl_slist_free_all(addresses);return result;
}

static json_object *request(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,struct curl_slist *headers,long budget,cancellation *cancel,char *err,size_t n)
{
    char origin[1024],pin[65];int direct=vr_controller_link_target(c,origin,pin);
    uint64_t start=vr_millis();
    json_object *o=request_at(c,origin,pin,iface,method,path,body,headers,(!direct&&budget==10000)?3000:budget,cancel,err,n);
    if(!(cancel&&cancel->check&&cancel->check(cancel->arg)))vr_controller_link_feedback(c,direct,o!=NULL,vr_millis()-start);
    /* Only challenge issuance is safe without new credentials. Signed business
       requests are retried by call(), with a fresh one-time challenge. */
    if(!o&&!headers&&!strcmp(method,"GET")&&vr_controller_link_target(c,origin,pin)!=direct)
        o=request_at(c,origin,pin,iface,method,path,body,headers,budget,cancel,err,n);
    return o;
}

static int authenticate(const vr_config *c,const char *iface,const char *method,const char *path,char challenge[513],char signature[65],cancellation *cancel,char *err,size_t n)
{
    char challenge_path[320],text[2048];
    if(!*c->node_id || !*c->node_key || strlen(path)>1024 || (strcmp(method,"GET")&&strcmp(method,"POST"))) {snprintf(err,n,"controller credentials or method invalid");return -1;}
    snprintf(challenge_path,sizeof(challenge_path),"/api/probe/auth/challenge?node_id=%s",c->node_id);
    json_object *o=request(c,iface,"GET",challenge_path,NULL,NULL,10000,cancel,err,n);if(!o)return -1;
    *challenge=0;int bad=vr_copy(challenge,513,vr_string(o,"challenge")) || !*challenge;
    json_object_put(o);
    for(const unsigned char *p=(const unsigned char *)challenge;*p;p++)if(*p<33||*p>126)bad=1;
    if(bad){snprintf(err,n,"invalid controller challenge");return -1;}
    snprintf(text,sizeof(text),"%s\n%s\n%s\n%s",c->node_id,challenge,method,path);
    if(vr_hmac(c->node_key,text,signature)){snprintf(err,n,"authentication unavailable");return -1;}
    return 0;
}

int vr_controller_auth(const vr_config *c,const char *iface,const char *method,const char *path,char challenge[513],char signature[65],char *err,size_t n)
{return authenticate(c,iface,method,path,challenge,signature,NULL,err,n);}

static json_object *call_once(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,long budget,cancellation *cancel,char *err,size_t n)
{
    char challenge[513]={0},signature[65],header[768];
    if(authenticate(c,iface,method,path,challenge,signature,cancel,err,n))return NULL;
    struct curl_slist *headers=NULL,*next=NULL;
    const char *names[]={"X-Probe-Node-Id","X-Probe-Rand","X-Probe-Signature","Content-Type"};
    const char *values[]={c->node_id,challenge,signature,"application/json"};
    for(unsigned i=0;i<4;i++) {
        snprintf(header,sizeof(header),"%s: %s",names[i],values[i]);next=curl_slist_append(headers,header);
        if(!next){curl_slist_free_all(headers);snprintf(err,n,"authentication allocation failed");return NULL;}headers=next;
    }
    json_object *result=request(c,iface,method,path,body,headers,budget,cancel,err,n);
    curl_slist_free_all(headers);return result;
}

void vr_controller_link_refresh(const vr_config *c,const char *iface)
{
    if(strncmp(c->controller,"https://",8)||!vr_controller_link_refresh_due(c))return;
    char path[320],err[128],challenge[513],text[2048],signature[65],line[768];
    snprintf(path,sizeof(path),"/api/probe/auth/challenge?node_id=%s",c->node_id);
    json_object *o=request_at(c,c->controller,"",iface,"GET",path,NULL,NULL,3000,NULL,err,sizeof(err));
    struct curl_slist *headers=NULL;
    if(!o)goto done;
    if(vr_copy(challenge,sizeof(challenge),vr_string(o,"challenge"))||!*challenge)goto done;
    json_object_put(o);o=NULL;
    const char *endpoint="/api/probe/controller/endpoints";
    snprintf(text,sizeof(text),"%s\n%s\nGET\n%s",c->node_id,challenge,endpoint);
    if(vr_hmac(c->node_key,text,signature))goto done;
    const char *names[]={"X-Probe-Node-Id","X-Probe-Rand","X-Probe-Signature"};
    const char *values[]={c->node_id,challenge,signature};
    for(int i=0;i<3;i++) {snprintf(line,sizeof(line),"%s: %s",names[i],values[i]);struct curl_slist *next=curl_slist_append(headers,line);if(!next)goto done;headers=next;}
    o=request_at(c,c->controller,"",iface,"GET",endpoint,NULL,headers,3000,NULL,err,sizeof(err));
    if(o)vr_controller_link_store(c,o);
done:
    json_object_put(o);curl_slist_free_all(headers);vr_controller_link_refresh_done();
}

static json_object *call(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,long budget,cancellation *cancel,char *err,size_t n)
{
    vr_controller_link_refresh(c,iface);
    char before[1024],pin[65],after[1024];vr_controller_link_target(c,before,pin);
    json_object *o=call_once(c,iface,method,path,body,budget,cancel,err,n);
    vr_controller_link_target(c,after,pin);
    if(!o&&!strcmp(method,"GET")&&strcmp(before,after)&&!(cancel&&cancel->check&&cancel->check(cancel->arg)))
        o=call_once(c,iface,method,path,body,budget,cancel,err,n);
    return o;
}

json_object *vr_controller_call(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,char *err,size_t n)
{return call(c,iface,method,path,body,10000,NULL,err,n);}

json_object *vr_controller_certificate(const vr_config *c,const char *iface,int cached,int (*cancelled)(void *),void *arg,char *err,size_t n)
{
    cancellation cancel={cancelled,arg};
    return call(c,iface,"GET","/api/probe/certificate",NULL,cached?10000:480000,&cancel,err,n);
}
