#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

static int platform_fail,platform_calls,delivered,request_started,release_request;
static pthread_mutex_t request_mutex=PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t request_changed=PTHREAD_COND_INITIALIZER;
int __wrap_vr_platform_routes_update(json_object *old,json_object *next)
{assert(old&&next);platform_calls++;return platform_fail;}
void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{(void)l;(void)f;(void)path;(void)n;delivered++;}
json_object *__wrap_vr_controller_call(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,char *err,size_t cap)
{
    (void)c;(void)iface;(void)method;(void)path;(void)body;(void)err;(void)cap;
    pthread_mutex_lock(&request_mutex);request_started=1;pthread_cond_signal(&request_changed);
    while(!release_request)pthread_cond_wait(&request_changed,&request_mutex);
    pthread_mutex_unlock(&request_mutex);
    char expiry[32];time_t now=time(NULL)+300;struct tm tm;gmtime_r(&now,&tm);strftime(expiry,sizeof(expiry),"%Y-%m-%dT%H:%M:%SZ",&tm);
    json_object *o=json_object_new_object(),*m=json_object_new_object();
    json_object_object_add(o,"item",m);
    json_object_object_add(m,"domain",json_object_new_string("other.example"));
    json_object_object_add(m,"fake_ip",json_object_new_string("198.18.1.21"));
    json_object_object_add(m,"rule_id",json_object_new_string("proxy"));
    json_object_object_add(m,"exit_node_id",json_object_new_string("8"));
    json_object_object_add(m,"action",json_object_new_string("probe_exit"));
    json_object_object_add(m,"expires_at",json_object_new_string(expiry));return o;
}
static json_object *copy(json_object *o)
{const char *s=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);return vr_json_parse(s,strlen(s));}
static json_object *route(json_object *snapshot)
{return json_object_array_get_idx(vr_member(vr_member(snapshot,"virtual_router"),"route_rules"),0);}
static json_object *link_rule(json_object *snapshot,int index)
{return json_object_array_get_idx(vr_member(vr_member(snapshot,"virtual_router"),"topology_rules"),(size_t)index);}
static json_object *pending(vr_engine *e)
{
    pthread_mutex_lock(&e->mutex);json_object_put(e->next_policy);e->next_policy=copy(e->snapshot);e->policy_retry=0;
    pthread_mutex_unlock(&e->mutex);return e->next_policy;
}
static void *late_mapping(void *arg)
{
    vr_engine *e=arg;vr_decision d=vr_policy_match(e->policy,"other.example",NULL);d.revision=e->policy_revision;
    vr_mapping m;assert(vr_mapping_get(e,"other.example",NULL,d,&m));return NULL;
}
int main(void)
{
    vr_service s={0};pthread_mutex_init(&s.mutex,NULL);
    vr_engine *e=calloc(1,sizeof(*e));assert(e);e->service=&s;pthread_mutex_init(&e->mutex,NULL);
    vr_config_default(&e->config);strcpy(e->config.node_id,"7");strcpy(e->config.node_key,"fixture-key");
    strcpy(e->own_ip,"198.18.0.7");strcpy(e->cidr,"198.18.0.0/15");
    const char *text="{\"node_id\":\"7\",\"expected_node_kind\":\"asus_56uv2\",\"virtual_router\":{\"enabled\":true,\"fake_ip_cidr\":\"198.18.0.0/15\",\"probe_ips\":[{\"node_id\":\"7\",\"ip\":\"198.18.0.7\"},{\"node_id\":\"8\",\"ip\":\"198.18.0.8\"},{\"node_id\":\"9\",\"ip\":\"198.18.0.9\"}],\"topology_rules\":[{\"id\":\"one\",\"from_node_id\":\"7\",\"to_node_id\":\"8\",\"secret\":\"fixture\",\"user_public_key\":\"fixture\",\"auth_ticket\":\"fixture\",\"enabled\":true},{\"id\":\"two\",\"from_node_id\":\"9\",\"to_node_id\":\"7\",\"secret\":\"fixture\",\"user_public_key\":\"fixture\",\"auth_ticket\":\"fixture\",\"enabled\":true},{\"id\":\"remote\",\"from_node_id\":\"8\",\"to_node_id\":\"9\",\"enabled\":true}],\"route_rules\":[{\"id\":\"proxy\",\"action\":\"probe_exit\",\"exit_node_id\":\"8\",\"entries\":[\"domain_suffix:example\"]}]}}";
    e->snapshot=vr_json_parse(text,strlen(text));assert(e->snapshot);e->policy=vr_member(e->snapshot,"virtual_router");
    assert(!vr_links_add(e,link_rule(e->snapshot,0))&&!vr_links_add(e,link_rule(e->snapshot,1)));
    SSL_CTX *tls=SSL_CTX_new(TLS_method());assert(tls);int others[8];SSL *connections[8];uint64_t sessions[8];vr_tx_frame *queued[8];
    for(int i=0;i<8;i++) {
        int pair[2];assert(!socketpair(AF_UNIX,SOCK_STREAM,0,pair));e->links[i].fd=pair[0];others[i]=pair[1];
        connections[i]=e->links[i].ssl=SSL_new(tls);sessions[i]=e->links[i].session=++e->carrier_session;
        assert(!vr_link_queue(&e->links[i],2,"abc",3));queued[i]=e->links[i].tx_head[1];e->links[i].auth_expires=time(NULL)+123;
        e->links[i].tx=345;e->links[i].rx=678;
    }
    e->flows[0].used=123;e->bindings[0].used=456;e->bindings[0].link=3;e->bindings[0].session=sessions[3];
    vr_mapping *m=&e->mappings[0];strcpy(m->domain,"cached.example");strcpy(m->ip,"198.18.1.20");strcpy(m->rule,"proxy");strcpy(m->exit,"8");m->expires=time(NULL)+100;
    time_t expires=m->expires,auth=e->links[0].auth_expires;
    json_object *next=pending(e);json_object_object_add(route(next),"exit_node_id",json_object_new_string("9"));
    e->handshakes[0].started=1;assert(!vr_policy_apply_pending(e)&&!e->policy_revision&&platform_calls==0);e->handshakes[0].started=0;
    platform_fail=1;assert(!vr_policy_apply_pending(e)&&!e->policy_revision&&!strcmp(m->exit,"8"));platform_fail=0;e->policy_retry=0;
    assert(vr_policy_apply_pending(e)==1&&!e->next_policy&&!e->refresh);
    assert(m->expires==expires&&!strcmp(m->exit,"9")&&e->flows[0].used==123&&e->bindings[0].used==456);
    for(int i=0;i<8;i++) {
        vr_link *l=&e->links[i];assert(l->ssl==connections[i]&&l->session==sessions[i]&&l->tx==345&&l->rx==678&&l->used==5&&l->tx_head[1]==queued[i]&&l->tx_bytes[1]==5);
        assert(write(l->fd,"x",1)==1);char b;assert(read(others[i],&b,1)==1&&b=='x');
    }
    next=pending(e);json_object_object_add(link_rule(next,0),"auth_ticket",json_object_new_string("fresh-ticket"));
    json_object_object_add(link_rule(next,0),"updated_at",json_object_new_string("new"));
    assert(vr_policy_apply_pending(e)==1&&e->links[0].ssl==connections[0]&&e->links[0].auth_expires==auth);
    /* A previously authorized multi-hop frame must revalidate removed edges. */
    const char *control="{\"path\":[\"8\",\"9\",\"7\"]}";vr_frame frame={99,0,(const uint8_t *)control,NULL,strlen(control),0};
    assert(!vr_link_receive(&frame,&e->links[4])&&delivered==1&&e->links[4].ingress_count==3);
    next=pending(e);json_object_object_add(link_rule(next,2),"enabled",json_object_new_boolean(0));
    assert(vr_policy_apply_pending(e)==1&&e->links[4].ssl==connections[4]&&!e->links[4].ingress_count);
    assert(!vr_link_receive(&frame,&e->links[4])&&delivered==1);
    const char *fields[]={"secret","user_public_key","to_tls_spki_sha256","to_service_domain","route_layer"};
    for(size_t field=0;field<sizeof(fields)/sizeof(*fields);field++) {
        if(field)for(int i=0;i<4;i++)e->links[i].ssl=SSL_new(tls);
        next=pending(e);json_object_object_add(link_rule(next,0),fields[field],json_object_new_string("rotated"));
        assert(vr_policy_apply_pending(e)==1);
        for(int i=0;i<8;i++)assert(i<4?!e->links[i].ssl:e->links[i].ssl==connections[i]);
    }
    for(int i=0;i<4;i++)e->links[i].ssl=SSL_new(tls);
    next=pending(e);json_object_object_add(json_object_array_get_idx(vr_member(vr_member(next,"virtual_router"),"probe_ips"),1),"service_port",json_object_new_int(15001));
    assert(vr_policy_apply_pending(e)==1&&!e->links[0].ssl&&e->links[4].ssl==connections[4]);
    next=pending(e);json_object_object_add(link_rule(next,0),"enabled",json_object_new_boolean(0));
    assert(vr_policy_apply_pending(e)==1&&e->link_count==8&&!e->links[0].rule&&e->links[4].ssl==connections[4]);
    next=pending(e);json_object_object_add(link_rule(next,0),"id",json_object_new_string("replacement"));json_object_object_add(link_rule(next,0),"enabled",json_object_new_boolean(1));
    assert(vr_policy_apply_pending(e)==1&&e->link_count==8&&e->links[0].rule&&e->links[4].ssl==connections[4]);
    next=pending(e);json_object_object_add(link_rule(next,1),"carrier_count",json_object_new_int(1));
    assert(vr_policy_apply_pending(e)==1&&e->links[4].ssl==connections[4]);
    for(int i=5;i<8;i++)assert(!e->links[i].ssl);
    /* An old controller reply must not repopulate mappings after a policy swap. */
    next=pending(e);json_object_object_add(route(next),"exit_node_id",json_object_new_string("8"));assert(vr_policy_apply_pending(e)==1);
    pthread_t thread;assert(!pthread_create(&thread,NULL,late_mapping,e));
    pthread_mutex_lock(&request_mutex);while(!request_started)pthread_cond_wait(&request_changed,&request_mutex);pthread_mutex_unlock(&request_mutex);
    next=pending(e);json_object_object_add(route(next),"action",json_object_new_string("reject"));assert(vr_policy_apply_pending(e)==1&&!m->expires);
    pthread_mutex_lock(&request_mutex);release_request=1;pthread_cond_signal(&request_changed);pthread_mutex_unlock(&request_mutex);pthread_join(thread,NULL);
    for(size_t i=0;i<VR_MAPPING_MAX;i++)assert(!e->mappings[i].expires);
    /* Foundation changes retain the validated snapshot for a full rebuild. */
    for(int variant=0;variant<5;variant++) {
        next=pending(e);json_object *policy=vr_member(next,"virtual_router");
        if(variant==0)json_object_object_add(policy,"fake_ip_cidr",json_object_new_string("198.19.0.0/16"));
        if(variant==1)json_object_object_add(policy,"enabled",json_object_new_boolean(0));
        if(variant==2)json_object_object_add(json_object_array_get_idx(vr_member(policy,"probe_ips"),0),"ip",json_object_new_string("198.18.0.17"));
        if(variant==3)json_object_object_add(json_object_array_get_idx(vr_member(policy,"probe_ips"),0),"service_port",json_object_new_int(12041));
        if(variant==4)json_object_object_add(next,"dns_upstreams",json_object_new_object());
        assert(vr_policy_apply_pending(e)==2&&e->refresh&&s.pending_policy==next&&!e->next_policy);
        json_object_put(s.pending_policy);s.pending_policy=NULL;e->refresh=0;
    }
    for(size_t i=0;i<e->link_count;i++)vr_link_close(&e->links[i],"");
    for(int i=0;i<8;i++)close(others[i]);
    json_object_put(s.pending_policy);json_object_put(e->snapshot);pthread_mutex_destroy(&e->mutex);pthread_mutex_destroy(&s.mutex);free(e);SSL_CTX_free(tls);
    puts("PASS: policy hot update preserves sockets, TLS sessions, queues, NAT and flow affinity; precise topology changes, cache TTL and stale-reply protection");
    return 0;
}
