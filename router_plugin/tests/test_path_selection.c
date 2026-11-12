#include "engine.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{vr_measure_frame(l->engine,l,f,path,n);}
static void expect(vr_engine *e,const char *middle,size_t length)
{char path[VR_PATH_MAX][129];size_t n;assert(!vr_path(e,"4",path,&n)&&n==length&&!strcmp(path[1],middle));}
int main(void)
{
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
    puts("PASS: hops first, equal-hop RTT, unknown/ties, disconnected paths, flow affinity and endpoint-pinned probes");return 0;
}
