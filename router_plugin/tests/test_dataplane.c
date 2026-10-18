#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <assert.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static uint8_t carried[1500];static size_t carried_len;
static void tcp_checksums(uint8_t b[40])
{
    b[10]=b[11]=b[36]=b[37]=0;uint16_t c=vr_checksum(b,20);b[10]=(uint8_t)(c>>8);b[11]=(uint8_t)c;
    uint8_t pseudo[32];memcpy(pseudo,b+12,8);pseudo[8]=0;pseudo[9]=6;pseudo[10]=0;pseudo[11]=20;memcpy(pseudo+12,b+20,20);
    c=vr_checksum(pseudo,sizeof(pseudo));b[36]=(uint8_t)(c>>8);b[37]=(uint8_t)c;
}
json_object *__wrap_vr_controller_call(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,char *error,size_t capacity)
{
    (void)c;(void)iface;(void)method;(void)body;(void)error;(void)capacity;
    assert(!strcmp(path,"/api/probe/route/fake_ip/resolve"));usleep(200000);
    const char *reply="{\"item\":{\"domain\":\"203.0.113.5\",\"fake_ip\":\"198.18.0.20\",\"action\":\"probe_exit\",\"exit_node_id\":\"8\",\"expires_at\":\"2099-01-01T00:00:00Z\"}}";
    return vr_json_parse(reply,strlen(reply));
}
int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n)
{memset(path,0,VR_PATH_MAX*129);strcpy(path[0],e->config.node_id);strcpy(path[1],target);*n=2;return 0;}
int vr_send(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,const void *b,size_t len)
{(void)e;(void)path;assert(main==1&&sub==1&&n==2&&len<=sizeof(carried));memcpy(carried,b,len);carried_len=len;return 0;}

static vr_engine *engine(const char *id,const char *ip,int pipes[2])
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);pthread_mutex_init(&e->mutex,NULL);vr_config_default(&e->config);
    strcpy(e->config.node_id,id);strcpy(e->own_ip,ip);strcpy(e->cidr,"198.18.0.0/15");
    const char *policy="{\"probe_ips\":[{\"node_id\":\"7\",\"ip\":\"198.18.0.7\"},{\"node_id\":\"8\",\"ip\":\"198.18.0.8\"}]}";
    e->policy=vr_json_parse(policy,strlen(policy));assert(e->policy);assert(!pipe2(pipes,O_NONBLOCK|O_CLOEXEC));e->tun=pipes[1];
    vr_mapping *m=&e->mappings[0];strcpy(m->domain,"203.0.113.5");strcpy(m->ip,"198.18.0.20");strcpy(m->exit,"8");m->expires=time(NULL)+60;
    return e;
}

int main(void)
{
    int entry_pipe[2],exit_pipe[2];vr_engine *entry=engine("7","198.18.0.7",entry_pipe),*exit=engine("8","198.18.0.8",exit_pipe);
    const char *hex="450000210001400040117f98c0a83464c6120014ad9c0035000d52fd68656c6c6f";
    uint8_t packet[1500];size_t len=strlen(hex)/2;for(size_t i=0;i<len;i++){unsigned b;assert(sscanf(hex+2*i,"%2x",&b)==1);packet[i]=(uint8_t)b;}
    vr_packet original;assert(!vr_packet_parse(packet,len,&original));
    vr_outgoing(entry,packet,len);assert(carried_len==len);
    char path[VR_PATH_MAX][129]={{"7"},{"8"}};vr_frame frame={1,1,NULL,carried,0,carried_len};
    vr_incoming(exit,&frame,path,2);assert(read(exit_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    vr_packet request;assert(!vr_packet_parse(packet,len,&request));struct in_addr real;assert(inet_pton(AF_INET,"203.0.113.5",&real)==1&&request.dst==real.s_addr);
    /* Emulate a response after the kernel's physical-WAN NAT has been undone. */
    assert(!vr_packet_rewrite(packet,len,request.dst,request.src,request.dport,request.sport));
    carried_len=0;vr_outgoing(exit,packet,len);assert(carried_len==len);
    strcpy(path[0],"9");strcpy(path[1],"7");frame.data_len=carried_len;
    vr_incoming(entry,&frame,path,2);assert(read(entry_pipe[0],packet,sizeof(packet))<0);
    strcpy(path[0],"8");vr_incoming(entry,&frame,path,2);assert(read(entry_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    vr_packet reply;assert(!vr_packet_parse(packet,len,&reply));
    assert(reply.src==original.dst&&reply.dst==original.src&&reply.sport==original.dport&&reply.dport==original.sport);
    assert(!memcmp(packet+28,"hello",5));
    for(size_t i=0;i<VR_NAT_MAX;i++)entry->flows[i].used=0;
    vr_incoming(entry,&frame,path,2);assert(read(entry_pipe[0],packet,sizeof(packet))<0);
    /* Remote LAN access shares the IP frame, but checks source, endpoint and ACL. */
    vr_gateway *g=&exit->config.gateway;g->enabled=1;g->cidr_count=2;g->node_count=1;
    strcpy(g->cidrs[0],"192.168.50.0/24");strcpy(g->cidrs[1],"10.20.0.0/16");strcpy(g->nodes[0],"7");
    for(size_t i=0;i<len;i++){unsigned value;assert(sscanf(hex+2*i,"%2x",&value)==1);packet[i]=(uint8_t)value;}
    assert(!vr_packet_rewrite(packet,len,inet_addr("198.18.0.7"),inet_addr("192.168.50.1"),45000,53));
    frame.data=packet;frame.data_len=len;strcpy(path[0],"7");strcpy(path[1],"8");
    g->enabled=0;vr_incoming(exit,&frame,path,2);assert(read(exit_pipe[0],carried,sizeof(carried))<0);
    g->enabled=1;strcpy(path[0],"9");vr_incoming(exit,&frame,path,2);assert(read(exit_pipe[0],carried,sizeof(carried))<0);
    strcpy(path[0],"7");strcpy(path[1],"9");vr_incoming(exit,&frame,path,2);assert(read(exit_pipe[0],carried,sizeof(carried))<0);
    strcpy(path[1],"8");vr_incoming(exit,&frame,path,2);assert(read(exit_pipe[0],carried,sizeof(carried))==(ssize_t)len);
    vr_packet lan;assert(!vr_packet_parse(carried,len,&lan));assert(lan.dst==inet_addr("192.168.50.1"));
    assert(!vr_packet_rewrite(carried,len,lan.dst,lan.src,lan.dport,lan.sport));
    vr_outgoing(exit,carried,len);assert(carried_len==len);
    vr_packet response;assert(!vr_packet_parse(carried,len,&response));assert(response.src==inet_addr("192.168.50.1")&&response.dst==inet_addr("198.18.0.7")&&response.dport==45000);
    frame.data=carried;
    /* A slow authoritative miss must not block the network loop or grow the queue. */
    assert(!vr_dataplane_start(entry));entry->mappings[0].expires=0;carried_len=0;
    for(size_t i=0;i<len;i++){unsigned b;assert(sscanf(hex+2*i,"%2x",&b)==1);packet[i]=(uint8_t)b;}
    uint64_t started=vr_millis();for(unsigned i=0;i<64;i++)vr_outgoing(entry,packet,len);
    assert(vr_millis()-started<100&&!carried_len);
    pthread_mutex_lock(&entry->mutex);size_t queued=0;for(size_t i=0;i<16;i++)if(entry->resolves[i].state)queued++;pthread_mutex_unlock(&entry->mutex);assert(queued==16);
    while(!carried_len&&vr_millis()-started<3000){usleep(10000);vr_dataplane_tick(entry);}
    assert(carried_len==len);vr_dataplane_stop(entry);
    /* Ordinary CIDRs (not published gateway rules) select public exits. */
    const char *cidr_rule="[{\"id\":\"ordinary-cidr\",\"action\":\"probe_exit\",\"exit_node_id\":\"8\",\"entries\":[\"cidr:203.0.113.0/24\"]}]";
    json_object_object_add(entry->policy,"route_rules",vr_json_parse(cidr_rule,strlen(cidr_rule)));
    json_object_object_add(exit->policy,"route_rules",vr_json_parse(cidr_rule,strlen(cidr_rule)));
    assert(!vr_packet_rewrite(packet,len,inet_addr("192.168.52.100"),inet_addr("203.0.113.5"),46000,53));
    carried_len=0;vr_outgoing(entry,packet,len);assert(carried_len==len);
    frame.data=carried;frame.data_len=len;strcpy(path[0],"7");strcpy(path[1],"8");
    vr_incoming(exit,&frame,path,2);assert(read(exit_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    assert(!vr_packet_parse(packet,len,&request)&&request.dst==inet_addr("203.0.113.5"));
    assert(!vr_packet_rewrite(packet,len,request.dst,request.src,request.dport,request.sport));
    vr_outgoing(exit,packet,len);strcpy(path[0],"8");strcpy(path[1],"7");vr_incoming(entry,&frame,path,2);
    assert(read(entry_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    /* An exit assigned to self reinjects once for physical WAN NAT. */
    assert(!vr_packet_rewrite(packet,len,inet_addr("192.168.52.100"),inet_addr("203.0.113.5"),47000,53));
    carried_len=0;vr_outgoing(exit,packet,len);assert(!carried_len&&read(exit_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    const char *reject="[{\"id\":\"reject\",\"action\":\"reject\",\"entries\":[\"cidr:203.0.113.0/24\"]}]";
    json_object_object_add(entry->policy,"route_rules",vr_json_parse(reject,strlen(reject)));
    assert(!vr_packet_rewrite(packet,len,inet_addr("192.168.52.100"),inet_addr("203.0.113.5"),48000,53));
    carried_len=0;vr_outgoing(entry,packet,len);assert(!carried_len&&read(entry_pipe[0],packet,sizeof(packet))<0);
    /* A rule update must not move an established TCP stream to another exit. */
    json_object_object_add(entry->policy,"route_rules",vr_json_parse(cidr_rule,strlen(cidr_rule)));
    json_object_object_add(exit->policy,"route_rules",vr_json_parse(cidr_rule,strlen(cidr_rule)));
    uint8_t tcp[40]={0x45,0,0,40,0,0,0x40,0,64,6};tcp[32]=0x50;tcp[33]=2;tcp[27]=10;
    tcp_checksums(tcp);
    assert(!vr_packet_rewrite(tcp,sizeof(tcp),inet_addr("192.168.52.100"),inet_addr("203.0.113.5"),49000,443));
    vr_outgoing(entry,tcp,sizeof(tcp));assert(carried_len==sizeof(tcp));
    frame.data=carried;frame.data_len=carried_len;strcpy(path[0],"7");strcpy(path[1],"8");
    vr_incoming(exit,&frame,path,2);uint8_t wire[40];assert(read(exit_pipe[0],wire,sizeof(wire))==40);
    json_object_object_add(entry->policy,"route_rules",vr_json_parse(reject,strlen(reject)));
    json_object_object_add(exit->policy,"route_rules",vr_json_parse(reject,strlen(reject)));
    tcp[33]=0x10;tcp[27]=11;tcp_checksums(tcp);assert(!vr_packet_rewrite(tcp,sizeof(tcp),inet_addr("192.168.52.100"),inet_addr("203.0.113.5"),49000,443));
    carried_len=0;vr_outgoing(entry,tcp,sizeof(tcp));assert(carried_len==40);frame.data_len=40;
    vr_incoming(exit,&frame,path,2);assert(read(exit_pipe[0],wire,sizeof(wire))==40);
    vr_packet tcp_request;assert(!vr_packet_parse(wire,40,&tcp_request));
    assert(!vr_packet_rewrite(wire,40,tcp_request.dst,tcp_request.src,tcp_request.dport,tcp_request.sport));
    vr_outgoing(exit,wire,40);strcpy(path[0],"9");strcpy(path[1],"7");
    const char *moved="[{\"action\":\"probe_exit\",\"exit_node_id\":\"9\",\"entries\":[\"cidr:203.0.113.0/24\"]}]";
    json_object_object_add(entry->policy,"route_rules",vr_json_parse(moved,strlen(moved)));
    vr_incoming(entry,&frame,path,2);assert(read(entry_pipe[0],wire,sizeof(wire))<0);
    json_object_object_add(entry->policy,"route_rules",vr_json_parse(reject,strlen(reject)));
    strcpy(path[0],"8");vr_incoming(entry,&frame,path,2);
    assert(read(entry_pipe[0],wire,sizeof(wire))==40);
    /* A new SYN incarnation on the reused tuple observes the new reject rule. */
    tcp[33]=2;tcp[27]=99;tcp_checksums(tcp);assert(!vr_packet_rewrite(tcp,40,inet_addr("192.168.52.100"),inet_addr("203.0.113.5"),49000,443));
    carried_len=0;vr_outgoing(entry,tcp,40);assert(!carried_len);
    close(entry_pipe[0]);close(entry_pipe[1]);close(exit_pipe[0]);close(exit_pipe[1]);
    json_object_put(entry->policy);json_object_put(exit->policy);pthread_mutex_destroy(&entry->mutex);pthread_mutex_destroy(&exit->mutex);free(entry);free(exit);
    puts("PASS: LAN fake-IP NAT round trip, spoof rejection, bounded asynchronous mapping resolution");return 0;
}
