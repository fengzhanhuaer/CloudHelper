#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count)
{(void)l;(void)f;(void)path;(void)count;assert(!"snapshot must not dispatch frames");}

int main(int argc,char **argv)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);pthread_mutex_init(&e->mutex,NULL);strcpy(e->config.node_id,"7");e->config.port=12040;
    json_object *rules=json_object_new_array();
    for(unsigned route=0;route<VR_LINK_MAX;route++) {
        char rule[512];snprintf(rule,sizeof(rule),"{\"id\":\"vrouter-test-%u\",\"name\":\"fixture-%u\",\"from_node_id\":\"%s\",\"to_node_id\":\"%s\",\"to_service_domain\":\"peer.example\",\"from_service_domain\":\"source.example\",\"to_service_port\":12040,\"from_service_port\":12041,\"carrier_count\":4}",route,route,route%2?"8":"7",route%2?"7":"8");
        json_object *r=vr_json_parse(rule,strlen(rule));assert(r);json_object_array_add(rules,r);
        for(unsigned slot=0;slot<VR_CARRIER_SLOTS;slot++) {
            vr_link *l=&e->links[e->link_count++];l->rule=r;l->fd=-1;l->slot=(int)slot;assert(!vr_route_id(vr_string(r,"id"),l->route));
            strcpy(l->peer,"8");l->client=!(route%2);l->connected_at=vr_millis()-2500;l->rtt=12;l->session=route*4+slot+1;
            /* Snapshot code only checks whether TLS is present; no TLS I/O. */
            if(slot<3)l->ssl=(SSL *)1;else strcpy(l->error,"fixture disconnected");
            l->used=128;l->tx_memory=256;l->tx_peak=1024;l->tx_memory_peak=1152;l->tx_enqueued=10;l->tx_dequeued=8;l->tx_full=2;
            l->decoder.used=5;l->decoder.peak=256;
        }
    }
    vr_telemetry_publish(e);json_object *before=json_object_get(e->telemetry),*rows=vr_member(before,"relay_status"),*items=vr_member(vr_member(before,"virtual_router_buffers"),"items");
    assert(json_object_array_length(rows)==8&&json_object_array_length(items)==64);
    assert(strlen(json_object_to_json_string_ext(before,JSON_C_TO_STRING_PLAIN))>16384);
    assert(json_object_array_length(vr_member(json_object_array_get_idx(rows,0),"bridge_sessions"))==3);
    assert(vr_member(json_object_array_get_idx(rows,1),"listen_state"));
    if(argc==2&&!strcmp(argv[1],"--json")){puts(json_object_to_json_string_ext(before,JSON_C_TO_STRING_PLAIN));return 0;}
    e->buffers_reset=1;vr_telemetry_publish(e);assert(e->telemetry_epoch==1);
    json_object *after=vr_member(vr_member(e->telemetry,"virtual_router_buffers"),"items");
    assert(json_object_get_int(vr_member(json_object_array_get_idx(items,0),"peak_depth"))==1024);
    assert(json_object_get_int(vr_member(json_object_array_get_idx(after,0),"peak_depth"))==128);
    assert(json_object_get_int(vr_member(json_object_array_get_idx(after,0),"enqueued"))==0&&e->links[0].used==128&&e->links[0].ssl);
    for(unsigned i=0;i<4;i++)e->links[i].rule=NULL;
    vr_telemetry_publish(e);assert(json_object_array_length(vr_member(e->telemetry,"relay_status"))==7);
    char ip[46];assert(!vr_public_ip("8.8.8.8\n",AF_INET,ip,sizeof(ip))&&!strcmp(ip,"8.8.8.8"));
    const char *bad[]={"192.168.1.1","198.18.0.1","100.64.0.1","127.0.0.1","0.0.0.0","224.0.0.1","8.8.8.8 injected","<html>"};
    for(unsigned i=0;i<sizeof(bad)/sizeof(*bad);i++)assert(vr_public_ip(bad[i],AF_INET,ip,sizeof(ip)));
    assert(!vr_public_ip("2606:4700:4700::1111",AF_INET6,ip,sizeof(ip))&&vr_public_ip("fe80::1",AF_INET6,ip,sizeof(ip)));
    vr_addresses a={0};strcpy(a.lan,"192.168.52.1");strcpy(a.wan,"8.8.8.8");json_object *report=vr_probe_report(&e->config,&a,"/nonexistent");
    vr_report_addresses(report,"8.8.8.8","");assert(json_object_array_length(vr_member(report,"ipv4"))==2);
    vr_report_addresses(report,"1.1.1.1","");assert(json_object_array_length(vr_member(report,"ipv4"))==3);
    json_object_put(report);json_object_put(before);json_object_put(e->telemetry);json_object_put(rules);pthread_mutex_destroy(&e->mutex);free(e);
    puts("PASS: router telemetry 32 carriers, immutable snapshots, live queue reset and public-IP validation");return 0;
}
