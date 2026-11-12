#include "engine.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void submit(vr_engine *e,const char *text)
{json_object *o=vr_json_parse(text,strlen(text));assert(o);vr_monitor_submit(e,o);json_object_put(o);}
static json_object *result(vr_engine *e,unsigned timeout)
{
    uint64_t until=vr_millis()+timeout;char *text;
    while(!(text=vr_monitor_take(e))&&vr_millis()<until)poll(NULL,0,10);
    assert(text);json_object *o=vr_json_parse(text,strlen(text));free(text);assert(o);return o;
}
int main(int argc,char **argv)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);vr_service service={0};e->service=&service;
    pthread_mutex_init(&e->mutex,NULL);strcpy(e->config.node_id,"7");strcpy(e->addresses.wan_if,"lo");assert(!vr_monitor_start(e));
    /* Go omits the tasks field when no tasks exist. Stock json-c does not
       accept NULL in json_object_array_length, unlike modern host libraries. */
    submit(e,"{\"type\":\"network_monitor_tasks\"}");
    submit(e,"{\"type\":\"network_monitor_tasks\",\"tasks\":null}");
    submit(e,"{\"type\":\"network_monitor_tasks\",\"tasks\":[]}");
    if(argc==2&&!strcmp(argv[1],"--empty-tasks")) {
        vr_monitor_stop(e);pthread_mutex_destroy(&e->mutex);free(e);
        puts("PASS: omitted/null/empty controller tasks on stock json-c");return 0;
    }
    submit(e,"{\"type\":\"network_monitor_test\",\"request_id\":\"dual\",\"targets\":[\"127.0.0.1\",\"::1\",\"::1\"],\"count\":3,\"timeout_ms\":300}");
    json_object *o=result(e,3000),*rows=vr_member(o,"results");assert(!strcmp(vr_string(o,"request_id"),"dual")&&!strcmp(vr_string(o,"node_id"),"7"));
    assert(json_object_array_length(rows)==2);
    for(size_t i=0;i<2;i++){json_object *r=json_object_array_get_idx(rows,i);assert(json_object_get_int(vr_member(r,"sent"))==3);assert(json_object_get_int(vr_member(r,"received"))==3);assert(json_object_get_double(vr_member(r,"loss_percent"))==0);assert(vr_member(r,"latency_avg_ms"));assert(!strcmp(vr_string(r,"ip_family"),i?"ipv6":"ipv4"));}json_object_put(o);
    submit(e,"{\"type\":\"network_monitor_test\",\"request_id\":\"invalid\",\"targets\":[\"127.0.0.1; reboot\"]}");
    o=result(e,500);assert(!json_object_get_boolean(vr_member(o,"ok"))&&!*vr_string(o,"task_id"));json_object_put(o);
    /* No route is an error/100% loss, never a successful zero-ms sample. */
    submit(e,"{\"type\":\"network_monitor_test\",\"request_id\":\"loss\",\"targets\":[\"2001:db8::dead\"],\"count\":1,\"timeout_ms\":300}");
    o=result(e,1500);json_object *r=json_object_array_get_idx(vr_member(o,"results"),0);assert(json_object_get_int(vr_member(r,"received"))==0&&json_object_get_double(vr_member(r,"loss_percent"))==100&&*vr_string(r,"error"));json_object_put(o);
    submit(e,"{\"type\":\"network_monitor_tasks\",\"tasks\":[{\"id\":\"scheduled\",\"enabled\":true,\"targets\":[\"127.0.0.1\"],\"count\":1,\"cycle_sec\":5}]}");
    o=result(e,1500);assert(!strcmp(vr_string(o,"task_id"),"scheduled")&&json_object_get_int(vr_member(o,"cycle_sec"))==5);json_object_put(o);
    submit(e,"{\"type\":\"network_monitor_tasks\",\"tasks\":[{\"id\":\"cancel\",\"enabled\":true,\"targets\":[\"192.0.2.1\"],\"count\":10,\"timeout_ms\":5000}]}");
    poll(NULL,0,60);submit(e,"{\"type\":\"network_monitor_tasks\",\"tasks\":[]}");poll(NULL,0,150);
    char *pending;while((pending=vr_monitor_take(e)))free(pending);
    uint64_t start=vr_millis();vr_monitor_stop(e);assert(vr_millis()-start<500);pthread_mutex_destroy(&e->mutex);free(e);
    puts("PASS: real IPv4/IPv6 ICMP, duplicate targets, errors/loss, periodic tasks and bounded cancellation");return 0;
}
