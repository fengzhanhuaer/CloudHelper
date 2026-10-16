#include "service.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>

int main(void)
{
    vr_service s={0};pthread_mutex_init(&s.mutex,NULL);
    vr_service_log(&s,"link","connection failed");vr_service_log(&s,"link","connection failed");
    json_object *o=vr_service_logs(&s),*items=vr_member(o,"items");
    assert(json_object_array_length(items)==1);
    assert(json_object_get_int(vr_member(json_object_array_get_idx(items,0),"repeats"))==2);json_object_put(o);
    for(int i=0;i<80;i++){char message[32];snprintf(message,sizeof(message),"event %d",i);vr_service_log(&s,"service",message);}
    o=vr_service_logs(&s);items=vr_member(o,"items");assert(json_object_array_length(items)==64);
    assert(!strcmp(vr_string(json_object_array_get_idx(items,0),"message"),"event 16"));
    assert(!strcmp(vr_string(json_object_array_get_idx(items,63),"message"),"event 79"));json_object_put(o);
    vr_service_log(&s,"link","safe\nline");o=vr_service_logs(&s);items=vr_member(o,"items");
    assert(!strcmp(vr_string(json_object_array_get_idx(items,63),"message"),"safe line"));json_object_put(o);
    pthread_mutex_destroy(&s.mutex);puts("PASS: RAM-only log ring is bounded, ordered and merges repeated errors");return 0;
}
