#define _POSIX_C_SOURCE 200809L
#include "engine.h"
#include <assert.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

int main(void)
{
    vr_engine *e=calloc(1,sizeof(*e));vr_service s={0};assert(e);e->service=&s;pthread_mutex_init(&s.mutex,NULL);
    assert(sizeof(e->observation)<1024);
    assert(!vr_observe_begin(e,VR_OBS_PACKET_IN));assert(!e->observation.stages[VR_OBS_PACKET_IN].calls);
    assert(vr_observe_request(&s,61));assert(!vr_observe_request(&s,2));uint64_t now=vr_millis();vr_observe_refresh(e,now);
    for(unsigned i=0;i<70;i++){uint64_t token=vr_observe_begin(e,VR_OBS_PACKET_IN);vr_observe_end(e,VR_OBS_PACKET_IN,token,0,0);}
    assert(e->observation.stages[VR_OBS_PACKET_IN].calls==70&&e->observation.stages[VR_OBS_PACKET_IN].samples==2);
    vr_observe_refresh(e,now+2100);assert(!vr_observe_begin(e,VR_OBS_PACKET_IN));
    json_object *report=vr_observe_report(e,now+2100);assert(!json_object_get_boolean(vr_member(report,"active")));json_object_put(report);
    uint8_t info[104]={0};uint32_t rtt=80000,cwnd=72;memcpy(info+68,&rtt,4);memcpy(info+80,&cwnd,4);
    report=vr_tcp_info_decode(info,84);assert(json_object_get_int(vr_member(report,"cwnd_packets"))==72);assert(json_object_get_double(vr_member(report,"rtt_ms"))==80);
    assert(!vr_member(report,"retransmits"));json_object_put(report);
    report=vr_socket_tcp_info(-1);assert(!json_object_get_boolean(vr_member(report,"supported")));json_object_put(report);
    pthread_mutex_destroy(&s.mutex);free(e);return 0;
}
