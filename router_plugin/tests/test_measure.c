#include "engine.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
static uint64_t now=1000;
uint64_t __wrap_vr_millis(void){return now;}
int vr_path(vr_engine *e,const char *id,char path[VR_PATH_MAX][129],size_t *n)
{(void)e;(void)id;(void)path;(void)n;return -1;}
int vr_send(vr_engine *e,uint16_t a,uint16_t b,char path[VR_PATH_MAX][129],size_t n,const void *data,size_t size)
{(void)e;(void)a;(void)b;(void)path;(void)n;(void)data;(void)size;return 0;}
int vr_send_json(vr_engine *e,uint16_t a,uint16_t b,char path[VR_PATH_MAX][129],size_t n,json_object *m)
{(void)e;(void)a;(void)b;(void)path;(void)n;(void)m;return 0;}
int main(void)
{
    vr_engine *e=calloc(1,sizeof(*e));vr_service s={0};assert(e);e->service=&s;strcpy(e->config.node_id,"7");
    pthread_mutex_init(&s.mutex,NULL);strcpy(e->measure_id,"test");e->measure_started=now;
    s.measurement=json_object_new_object();json_object_object_add(s.measurement,"running",json_object_new_boolean(1));
    json_object_object_add(s.measurement,"kind",json_object_new_string("download"));
    char path[VR_PATH_MAX][129]={{"8"},{"7"}};
    const char *start="{\"request_id\":\"test\",\"result_node_id\":\"7\",\"max_duration_ms\":3000}";
    vr_frame f={4,1,NULL,(const uint8_t *)start,0,strlen(start)};vr_measure_frame(e,NULL,&f,path,2);
    uint8_t chunk[1024]={'V','R','S','1',0,4,'t','e','s','t'};f.sub=2;f.data=chunk;f.data_len=sizeof(chunk);
    vr_measure_frame(e,NULL,&f,path,2);now+=1000;vr_measure_frame(e,NULL,&f,path,2);
    now=4499;vr_measure_tick(e);assert(json_object_get_boolean(vr_member(s.measurement,"running")));
    now=4500;vr_measure_tick(e);json_object *r=vr_member(s.measurement,"result");
    assert(!json_object_get_boolean(vr_member(s.measurement,"running"))&&json_object_get_boolean(vr_member(r,"ok")));
    assert(json_object_get_int(vr_member(r,"bytes"))==2048&&json_object_get_int(vr_member(r,"duration_ms"))==1000);
    f.sub=3;f.data=(const uint8_t *)start;f.data_len=strlen(start);vr_measure_frame(e,NULL,&f,path,2);
    assert(vr_member(s.measurement,"result")==r); /* delayed finish cannot overwrite */
    json_object_put(s.measurement);pthread_mutex_destroy(&s.mutex);free(e);
    puts("PASS: receive-window measurement completes without delayed finish and ignores late finish");return 0;
}
