#include "engine.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static unsigned delivered;
static unsigned json_objects;
static uint8_t last_wire[512];static size_t last_wire_length;
json_object *__real_json_object_new_object(void);
json_object *__wrap_json_object_new_object(void)
{json_objects++;return __real_json_object_new_object();}
int __real_vr_frame_encode(const vr_frame *,uint8_t *,size_t,size_t *);
int __wrap_vr_frame_encode(const vr_frame *frame,uint8_t *out,size_t cap,size_t *length)
{
    int result=__real_vr_frame_encode(frame,out,cap,length);
    if(!result&&*length<=sizeof(last_wire)){memcpy(last_wire,out,*length);last_wire_length=*length;}
    return result;
}

static void test_packet_reuse(int binary)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);vr_service s={0};e->service=&s;
    pthread_mutex_init(&s.mutex,NULL);strcpy(e->config.node_id,"1");
    const char *text="{\"probe_ips\":[{\"node_id\":\"1\"},{\"node_id\":\"2\"},{\"node_id\":\"3\"}],\"topology_rules\":["
        "{\"enabled\":true,\"from_node_id\":\"1\",\"to_node_id\":\"2\",\"user_id\":\"A\"},"
        "{\"enabled\":true,\"from_node_id\":\"2\",\"to_node_id\":\"3\",\"user_id\":\"A\"}]}";
    e->policy=vr_json_parse(text,strlen(text));assert(e->policy);
    json_object *rules=vr_member(e->policy,"topology_rules");
    SSL_CTX *ctx=SSL_CTX_new(TLS_method());assert(ctx);vr_link *l=&e->links[0];e->link_count=1;
    l->engine=e;l->fd=-1;l->ssl=SSL_new(ctx);assert(l->ssl);l->session=1;l->binary_path=binary;l->rule=json_object_array_get_idx(rules,0);
    strcpy(l->peer,"2");strcpy(l->route,"same-route");
    uint8_t packet[40]={0x45,0,0,40,0,0,0,0,64,6,0,0,10,0,0,1,10,0,0,2,3,232,1,187};packet[32]=0x50;packet[33]=0x10;
    char path[VR_PATH_MAX][129]={{"1"},{"2"},{"3"}};size_t count;
    assert(!vr_send(e,1,1,path,3,packet,sizeof(packet)));
    assert(last_wire[12]==(binary?0:'{'));
    unsigned allocations=json_objects;packet[27]++;
    assert(!vr_packet_path(e,"3",packet,sizeof(packet),path,&count)&&count==3);
    assert(!vr_send(e,1,1,path,count,packet,sizeof(packet)));
    assert(json_objects==allocations); /* No JSON allocation per established ACK. */
    assert(last_wire_length>=sizeof(packet)&&!memcmp(last_wire+last_wire_length-sizeof(packet),packet,sizeof(packet)));
    /* A changed explicit path cannot borrow the cached authorization. */
    strcpy(path[2],"9");size_t queued=l->used;
    assert(vr_send(e,1,1,path,3,packet,sizeof(packet))<0&&queued==l->used);strcpy(path[2],"3");
    /* Reconnected slots must go through authorization again. */
    allocations=json_objects;l->session++;
    assert(!vr_send(e,1,1,path,3,packet,sizeof(packet)));if(!binary)assert(json_objects>allocations);
    /* Publication revokes a removed downstream edge immediately. */
    json_object *edge=json_object_array_get_idx(rules,1);e->policy_revision++;
    json_object_object_add(edge,"enabled",json_object_new_boolean(0));queued=l->used;
    assert(vr_send(e,1,1,path,3,packet,sizeof(packet))<0&&queued==l->used);
    json_object_object_add(edge,"enabled",json_object_new_boolean(1));e->policy_revision++;
    assert(!vr_send(e,1,1,path,3,packet,sizeof(packet)));
    /* TCP stream frames and other subtypes never use the packet fast path. */
    allocations=json_objects;assert(!vr_send(e,1,2,path,3,packet,sizeof(packet)));if(!binary)assert(json_objects>allocations);
    vr_link_close(l,"");assert(vr_send(e,1,1,path,3,packet,sizeof(packet))<0);
    json_object_put(e->policy);pthread_mutex_destroy(&s.mutex);SSL_CTX_free(ctx);free(e);
}
void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{delivered++;vr_measure_frame(l->engine,l,f,path,n);}
static void expect(vr_engine *e,const char *middle,size_t length)
{char path[VR_PATH_MAX][129];size_t n;assert(!vr_path(e,"4",path,&n)&&n==length&&!strcmp(path[1],middle));}

static void test_owner_authorization(void)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);vr_service s={0};e->service=&s;
    pthread_mutex_init(&s.mutex,NULL);strcpy(e->config.node_id,"7");
    const char *text="{\"probe_ips\":[],\"topology_rules\":["
        "{\"id\":\"ingress\",\"enabled\":true,\"from_node_id\":\"8\",\"to_node_id\":\"7\",\"user_id\":\"A\"},"
        "{\"id\":\"foreign-prefix\",\"enabled\":true,\"from_node_id\":\"9\",\"to_node_id\":\"8\",\"user_id\":\"B\"},"
        "{\"id\":\"egress-a\",\"enabled\":true,\"from_node_id\":\"7\",\"to_node_id\":\"6\",\"user_id\":\"A\"},"
        "{\"id\":\"egress-b\",\"enabled\":true,\"from_node_id\":\"7\",\"to_node_id\":\"6\",\"user_id\":\"B\"},"
        "{\"id\":\"return-prefix\",\"enabled\":true,\"from_node_id\":\"10\",\"to_node_id\":\"8\",\"user_id\":\"A\"},"
        "{\"id\":\"return-b\",\"enabled\":true,\"from_node_id\":\"7\",\"to_node_id\":\"8\",\"user_id\":\"B\"}]}";
    e->policy=vr_json_parse(text,strlen(text));assert(e->policy);
    json_object *rules=vr_member(e->policy,"topology_rules");
    assert(!vr_links_add(e,json_object_array_get_idx(rules,0)));
    assert(!vr_links_add(e,json_object_array_get_idx(rules,2)));
    assert(!vr_links_add(e,json_object_array_get_idx(rules,3)));
    assert(!vr_links_add(e,json_object_array_get_idx(rules,5)));
    SSL_CTX *ctx=SSL_CTX_new(TLS_method());assert(ctx);
    vr_link *in=&e->links[0],*own=&e->links[VR_CARRIER_SLOTS],*other=&e->links[2*VR_CARRIER_SLOTS];
    in->ssl=SSL_new(ctx);own->ssl=SSL_new(ctx);other->ssl=SSL_new(ctx);
    assert(in->ssl&&own->ssl&&other->ssl);own->rtt=50;other->rtt=1;
    uint8_t packet[28]={0x45,0,0,28,0,0,0,0,64,17,0,0,10,0,0,1,10,0,0,2,3,232,1,187,0,8,0,0};
    const char *foreign="{\"path\":[\"9\",\"8\",\"7\"]}";
    vr_frame f={1,1,(const uint8_t *)foreign,packet,strlen(foreign),sizeof(packet)};
    unsigned before=delivered;assert(!vr_link_receive(&f,in));assert(delivered==before&&in->ssl);
    const char *local="{\"path\":[\"8\",\"7\"]}";
    f.control=(const uint8_t *)local;f.control_len=strlen(local);
    assert(!vr_link_receive(&f,in));assert(delivered==before+1);
    f.control=NULL;f.control_len=0;assert(!vr_link_receive(&f,in));assert(delivered==before+2);
    const char *forward="{\"path\":[\"8\",\"7\",\"6\"]}";
    f.control=(const uint8_t *)forward;f.control_len=strlen(forward);
    /* A cached local flow or a faster foreign carrier cannot override ingress ownership. */
    assert(vr_carrier_select(e,"6",&f)==other);
    assert(!vr_link_receive(&f,in));assert(own->used&&other->used==0);
    size_t sent=own->used;assert(!vr_link_receive(&f,in));assert(own->used>sent&&other->used==0);
    /* Every slot of the eligible route remains usable. */
    vr_link *slot=&e->links[VR_CARRIER_SLOTS+1];slot->ssl=SSL_new(ctx);assert(slot->ssl);
    vr_link_close(own,"");assert(!vr_link_receive(&f,in));assert(slot->used&&other->used==0);
    vr_link_close(slot,"");assert(!vr_link_receive(&f,in));assert(other->used==0);
    own->ssl=SSL_new(ctx);assert(own->ssl);
    /* Local multi-hop Ping responses must also retain a path's owner scope. */
    vr_link *return_other=&e->links[3*VR_CARRIER_SLOTS];return_other->ssl=SSL_new(ctx);assert(return_other->ssl);
    in->rtt=50;return_other->rtt=1;
    const char *request="{\"path\":[\"10\",\"8\",\"7\"]}",*ping="{\"request_id\":\"owner-ping\"}";
    vr_frame request_frame={2,1,(const uint8_t *)request,(const uint8_t *)ping,strlen(request),strlen(ping)};
    assert(vr_carrier_select(e,"8",&request_frame)==return_other);
    size_t replies=in->used;assert(!vr_link_receive(&request_frame,in));assert(in->used>replies&&return_other->used==0);
    char response_path[VR_PATH_MAX][129]={{"7"},{"8"},{"10"}};
    vr_link_close(in,"");
    assert(vr_send(e,2,2,response_path,3,ping,strlen(ping))<0&&return_other->used==0);
    in->ssl=SSL_new(ctx);assert(in->ssl);
    const char *encoded=json_object_to_json_string_ext(e->policy,JSON_C_TO_STRING_PLAIN);
    json_object *next=vr_json_parse(encoded,strlen(encoded));assert(next);
    json_object *changed=json_object_array_get_idx(vr_member(next,"topology_rules"),2);
    json_object_object_add(changed,"user_id",json_object_new_string("B"));
    vr_links_reconcile(e,next);assert(in->ssl&&!in->ingress_count&&!own->ssl);
    json_object_put(e->policy);e->policy=next;
    before=delivered;assert(!vr_link_receive(&f,in));assert(delivered==before&&other->used==0);
    for(size_t i=0;i<e->link_count;i++)vr_link_close(&e->links[i],"");
    json_object_put(e->policy);pthread_mutex_destroy(&s.mutex);SSL_CTX_free(ctx);free(e);
}
int main(void)
{
    test_packet_reuse(0);test_packet_reuse(1);
    test_owner_authorization();
    vr_engine *e=calloc(1,sizeof(*e));assert(e);vr_service s={0};e->service=&s;pthread_mutex_init(&s.mutex,NULL);strcpy(e->config.node_id,"1");
    const char *text="{\"probe_ips\":[{\"node_id\":\"1\"},{\"node_id\":\"3\"},{\"node_id\":\"2\"},{\"node_id\":\"4\"}],\"topology_rules\":[{\"enabled\":true,\"from_node_id\":\"1\",\"to_node_id\":\"2\"},{\"enabled\":true,\"from_node_id\":\"1\",\"to_node_id\":\"3\"},{\"enabled\":true,\"from_node_id\":\"2\",\"to_node_id\":\"4\"},{\"enabled\":true,\"from_node_id\":\"3\",\"to_node_id\":\"4\"},{\"enabled\":true,\"from_node_id\":\"1\",\"to_node_id\":\"4\"}]}";
    e->policy=vr_json_parse(text,strlen(text));SSL_CTX *ctx=SSL_CTX_new(TLS_method());assert(ctx);e->link_count=4;
    const char *peers[]={"2","3","4","2"};
    for(size_t i=0;i<4;i++){vr_link *l=&e->links[i];l->engine=e;l->fd=-1;l->ssl=SSL_new(ctx);assert(l->ssl);strcpy(l->peer,peers[i]);snprintf(l->route,sizeof(l->route),"edge-%zu",i);l->session=i+1;l->rtt=i==0?80:i==1?20:i==2?200:10;}
    expect(e,"4",2); /* 200ms direct beats faster two-hop. */
    vr_link_close(&e->links[2],"");expect(e,"2",3); /* fast parallel carrier */
    e->links[3].rtt=100;expect(e,"3",3); /* equal hops compare RTT */
    e->links[0].rtt=0;e->links[3].rtt=0;expect(e,"3",3); /* unknown is not zero */
    e->links[0].rtt=20;expect(e,"2",3); /* deterministic node-ID tie */
    /* Full-path samples override the first-hop estimate. */
    uint64_t now=vr_millis();
    for(size_t i=0;i<VR_PATH_RTT_MAX;i++){vr_path_rtt *r=&e->path_rtt[i];if(r->count==3){r->rtt=r->nodes[1]==2?300:50;r->updated=now;}}
    expect(e,"3",3);
    /* A measured path cannot rescue a disconnected first hop. */
    vr_link_close(&e->links[1],"");expect(e,"2",3);
    uint8_t packet[28]={0x45,0,0,28,0,0,0,0,64,17,0,0,10,0,0,1,10,0,0,2,3,232,1,187,0,8,0,0};
    vr_frame f={1,1,NULL,packet,0,sizeof(packet)};vr_link *first=vr_carrier_select(e,"2",&f);assert(first==&e->links[0]);
    e->links[3].rtt=1;assert(vr_carrier_select(e,"2",&f)==first); /* keep existing flow */
    packet[21]++;assert(vr_carrier_select(e,"2",&f)==&e->links[3]); /* new flow uses faster endpoint */
    char path[VR_PATH_MAX][129]={{"1"},{"2"},{"4"}};size_t count;
    assert(!vr_send(e,1,1,path,3,packet,sizeof(packet)));
    e->links[1].ssl=SSL_new(ctx);e->links[1].session++;e->links[1].rtt=1;
    for(size_t i=0;i<VR_PATH_RTT_MAX;i++){vr_path_rtt *r=&e->path_rtt[i];if(r->count==3){r->rtt=r->nodes[1]==2?300:1;r->updated=vr_millis();}}
    expect(e,"3",3);assert(!vr_packet_path(e,"4",packet,sizeof(packet),path,&count)&&count==3&&!strcmp(path[1],"2"));
    /* Each endpoint must receive its own RTT request, including the slower one. */
    e->links[0].ping_time=e->links[3].ping_time=0;vr_measure_tick(e);
    assert(e->links[0].used&&e->links[3].used&&*e->links[0].ping_id&&*e->links[3].ping_id);
    for(size_t i=0;i<4;i++)vr_link_close(&e->links[i],"");
    json_object_put(e->policy);pthread_mutex_destroy(&s.mutex);SSL_CTX_free(ctx);free(e);
    puts("PASS: owner-scoped ingress/forwarding/cache/slots, owner changes, hops first, equal-hop RTT, flow affinity and endpoint-pinned probes");return 0;
}
