#define _GNU_SOURCE
#include <assert.h>
#include <curl/curl.h>
#include <stdarg.h>
static long response_code=200;
static CURLcode fixture_getinfo(CURL *curl,CURLINFO info,...)
{
    (void)curl;assert(info==CURLINFO_RESPONSE_CODE);va_list ap;va_start(ap,info);*va_arg(ap,long *)=response_code;va_end(ap);return CURLE_OK;
}
#undef curl_easy_getinfo
#define curl_easy_getinfo fixture_getinfo
#include "../src/external_speed.c"
#include <assert.h>
int vr_path(vr_engine *e,const char *node,char path[VR_PATH_MAX][129],size_t *count)
{(void)e;(void)node;(void)path;(void)count;return -1;}
int main(void)
{
    vr_engine *e=calloc(1,sizeof(*e));vr_service s={0};assert(e);e->service=&s;
    pthread_mutex_init(&s.mutex,NULL);pthread_mutex_init(&e->mutex,NULL);
    struct vr_external_state x={.engine=e,.start=vr_millis(),.curl=(CURL *)1,.fd=-1};
    char bytes[8192]={0};response_code=403;assert(!receive_data(bytes,1,sizeof(bytes),&x)&&!x.bytes);
    response_code=200;assert(receive_data(bytes,1,sizeof(bytes),&x)==sizeof(bytes)&&x.bytes==sizeof(bytes));
    x.bytes=EXTERNAL_BYTES-3;assert(!receive_data(bytes,1,sizeof(bytes),&x)&&x.bytes==EXTERNAL_BYTES);
    x.bytes=8192;x.first=vr_millis()-EXTERNAL_MS-1;assert(progress(&x,0,0,0,0)&&!receive_data(bytes,1,sizeof(bytes),&x)&&x.bytes==8192);
    s.measure_cancel=1;assert(progress(&x,0,0,0,0));s.measure_cancel=0;
    x.source=inet_addr("198.18.0.7");x.destination=inet_addr("198.19.255.254");x.resolved=inet_addr("1.1.1.1");x.port=40001;x.active=1;strcpy(x.node,"8");e->external=&x;
    vr_packet p={.src=x.source,.dst=x.destination,.sport=x.port,.dport=443,.protocol=6};uint32_t real=0;char node[129]={0};
    assert(vr_external_target(e,&p,&real,node)&&real==x.resolved&&!strcmp(node,"8"));
    x.http=1;
    assert(!vr_external_target(e,&p,&real,node));p.dport=80;
    assert(vr_external_target(e,&p,&real,node)&&real==x.resolved&&!strcmp(node,"8"));
    x.first=x.last=vr_millis();publish(&x,0,NULL,0);
    assert(!strcmp(vr_string(vr_member(s.measurement,"result"),"protocol"),"http"));
    x.http=0;p.dport=443;
    p.sport++;assert(!vr_external_target(e,&p,&real,node));p.sport--;p.protocol=17;assert(!vr_external_target(e,&p,&real,node));p.protocol=6;x.active=0;assert(!vr_external_target(e,&p,&real,node));
    x.first=x.last=vr_millis();publish(&x,0,NULL,0);assert(!json_object_get_boolean(vr_member(s.measurement,"running")));
    assert(json_object_get_boolean(vr_member(vr_member(s.measurement,"result"),"ok")));
    json_object_put(s.measurement);pthread_mutex_destroy(&e->mutex);pthread_mutex_destroy(&s.mutex);free(e);
    puts("PASS: external HTTP/HTTPS status, byte/time/cancel limits and exact TCP tuple isolation");return 0;
}
