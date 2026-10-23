#define _GNU_SOURCE
#include "chvr.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

static json_object *response;static unsigned calls;
json_object *__wrap_vr_controller_call(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,char *err,size_t n)
{calls++;(void)c;(void)iface;(void)method;(void)path;(void)body;if(n)*err=0;return json_object_get(response);}

int main(void)
{
    const char *raw="{\"node_id\":\"7\",\"expected_node_kind\":\"asus_56uv2\",\"virtual_router\":{\"enabled\":true,\"fake_ip_cidr\":\"198.18.0.0/15\",\"probe_ips\":[{\"node_id\":\"7\",\"ip\":\"198.18.0.7\"}],\"topology_rules\":[],\"route_rules\":[{\"id\":\"gateway-proxy-23-test\",\"action\":\"probe_exit\",\"exit_node_id\":\"23\",\"entries\":[\"192.168.50.0/24\"]}]}}";
    char root[]="/tmp/chvr-cache-XXXXXX",path[256],err[256];assert(mkdtemp(root));snprintf(path,sizeof(path),"%s/routes.json",root);
    vr_config c;vr_config_default(&c);strcpy(c.node_id,"7");response=vr_json_parse(raw,strlen(raw));assert(response);
    json_object *o=vr_policy_fetch_cached(&c,"",path,err,sizeof(err));assert(o);json_object_put(o);
    struct stat before,after;assert(!stat(path,&before));assert((before.st_mode&0777)==0600);
    o=vr_policy_fetch_cached(&c,"",path,err,sizeof(err));assert(o);json_object_put(o);assert(!stat(path,&after)&&before.st_ino==after.st_ino);
    json_object *online=response;response=NULL;
    unsigned fetched=calls;
    o=vr_policy_fetch_pending(&c,"",path,json_object_get(online),err,sizeof(err));assert(o&&calls==fetched);json_object_put(o);
    json_object *rule=vr_json_parse("{\"id\":\"r\",\"enabled\":true,\"from_node_id\":\"7\",\"to_node_id\":\"8\",\"secret\":\"test-only\",\"auth_ticket\":\"fixture\",\"user_public_key\":\"fixture\",\"route_layer\":\"websocket\"}",strlen("{\"id\":\"r\",\"enabled\":true,\"from_node_id\":\"7\",\"to_node_id\":\"8\",\"secret\":\"test-only\",\"auth_ticket\":\"fixture\",\"user_public_key\":\"fixture\",\"route_layer\":\"websocket\"}"));
    assert(rule);json_object_array_add(vr_member(vr_member(online,"virtual_router"),"topology_rules"),rule);
    assert(!vr_policy_validate(online,"7",err,sizeof(err)));
    json_object_object_add(rule,"route_layer",json_object_new_string("quic"));assert(vr_policy_validate(online,"7",err,sizeof(err))<0);
    json_object_object_add(rule,"route_layer",json_object_new_string("http2"));assert(!vr_policy_validate(online,"7",err,sizeof(err)));

    /* A ninth adjacent edge must survive a cold start, without dropping any
       existing policy. Keep a finite limit and reject the thirty-third edge. */
    json_object *links=vr_member(vr_member(online,"virtual_router"),"topology_rules");
    assert(VR_LINK_MAX>=9);
    for(unsigned i=1;i<=VR_LINK_MAX;i++) {
        json_object *extra=vr_json_parse(json_object_to_json_string(rule),strlen(json_object_to_json_string(rule)));assert(extra);
        char id[24];snprintf(id,sizeof(id),"capacity-%u",i);json_object_object_add(extra,"id",json_object_new_string(id));json_object_array_add(links,extra);
        if(i<VR_LINK_MAX)assert(!vr_policy_validate(online,"7",err,sizeof(err)));
        else assert(vr_policy_validate(online,"7",err,sizeof(err))<0&&strstr(err,"adjacent links"));
    }
    json_object_object_add(json_object_array_get_idx(links,VR_LINK_MAX),"enabled",json_object_new_boolean(0));
    assert(!vr_policy_validate(online,"7",err,sizeof(err)));
    json_object *restored=json_object_new_array();json_object_array_add(restored,json_object_get(rule));
    json_object_object_add(vr_member(online,"virtual_router"),"topology_rules",restored);

    o=vr_policy_fetch_cached(&c,"",path,err,sizeof(err));assert(o&&json_object_array_length(vr_member(vr_member(o,"virtual_router"),"route_rules"))==1);json_object_put(o);
    strcpy(c.node_id,"8");assert(!vr_policy_fetch_cached(&c,"",path,err,sizeof(err)));strcpy(c.node_id,"7");
    response=json_object_new_object();assert(!vr_policy_fetch_cached(&c,"",path,err,sizeof(err)));json_object_put(response);
    response=online;json_object_object_add(vr_member(response,"virtual_router"),"route_rules",json_object_new_array());
    o=vr_policy_fetch_cached(&c,"",path,err,sizeof(err));assert(o);json_object_put(o);response=NULL;
    o=vr_policy_fetch_cached(&c,"",path,err,sizeof(err));assert(o&&json_object_array_length(vr_member(vr_member(o,"virtual_router"),"route_rules"))==0);json_object_put(o);
    assert(!vr_atomic_write(path,"{",1));assert(!vr_policy_fetch_cached(&c,"",path,err,sizeof(err)));
    json_object_put(online);unlink(path);rmdir(root);puts("PASS: persisted routes recover offline, revocation replaces cache, wrong identity/corruption/invalid online responses fail closed");return 0;
}
