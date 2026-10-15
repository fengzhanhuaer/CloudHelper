#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

/* Include the module to exercise its internal rule pipeline as well as sockets. */
#include "../src/dns_service.c"
static int upstream_requests,mapping_requests;
int vr_platform_dns(int enabled){(void)enabled;return 0;}
int vr_bind4(const char *ip,int port,int tcp)
{
    int fd=socket(AF_INET,tcp?SOCK_STREAM:SOCK_DGRAM,0);assert(fd>=0);struct sockaddr_in a={0};a.sin_family=AF_INET;a.sin_port=htons(port);assert(inet_pton(AF_INET,ip,&a.sin_addr)==1);
    int yes=1;setsockopt(fd,SOL_SOCKET,SO_REUSEADDR,&yes,sizeof(yes));assert(!bind(fd,(struct sockaddr *)&a,sizeof(a)));if(tcp)assert(!listen(fd,8));return fd;
}
int vr_mapping_get(vr_engine *e,const char *domain,const char *ip,vr_decision d,vr_mapping *out)
{
    (void)e;(void)ip;assert(domain&&d.action==VR_PROXY&&!strcmp(d.exit_node,"8"));__sync_fetch_and_add(&mapping_requests,1);memset(out,0,sizeof(*out));strcpy(out->ip,"198.18.0.20");return 0;
}
static void *upstream(void *arg)
{
    int fd=*(int *)arg;uint8_t q[512],reply[512];
    for(;;){struct sockaddr_in peer;socklen_t size=sizeof(peer);ssize_t n=recvfrom(fd,q,sizeof(q),0,(struct sockaddr *)&peer,&size);if(n<=0)continue;
        vr_question parsed;assert(!vr_dns_question(q,(size_t)n,&parsed));size_t rn;__sync_fetch_and_add(&upstream_requests,1);
        assert(!vr_dns_reply(q,(size_t)n,reply,sizeof(reply),0,parsed.type==1?"203.0.113.5":NULL,&rn));assert(sendto(fd,reply,rn,0,(struct sockaddr *)&peer,size)==(ssize_t)rn);}
    return NULL;
}
static void policy(vr_engine *e,const char *text){json_object_put(e->policy);e->policy=vr_json_parse(text,strlen(text));assert(e->policy);}
static size_t query(uint8_t *q,int type)
{const uint8_t wire[]={0x12,0x34,1,0,0,1,0,0,0,0,0,0,7,'e','x','a','m','p','l','e',3,'c','o','m',0,0,1,0,1};memcpy(q,wire,sizeof(wire));q[sizeof(wire)-3]=(uint8_t)type;return sizeof(wire);}

int main(void)
{
    int fd=vr_bind4("127.0.0.1",53,0);pthread_t thread;assert(!pthread_create(&thread,NULL,upstream,&fd));
    vr_engine *e=calloc(1,sizeof(*e));assert(e);pthread_mutex_init(&e->mutex,NULL);pthread_cond_init(&e->jobs_changed,NULL);e->dns_udp=e->dns_tcp=-1;
    strcpy(e->config.dns_upstream,"127.0.0.1:53");uint8_t q[512],reply[512];size_t n=query(q,1),rn;char ip[16];
    policy(e,"{\"route_rules\":[]}");assert(!dns_answer(e,q,n,reply,sizeof(reply),&rn)&&!vr_dns_address4(reply,rn,ip)&&!strcmp(ip,"203.0.113.5"));assert(upstream_requests==1&&!mapping_requests);
    policy(e,"{\"route_rules\":[{\"id\":\"block\",\"action\":\"reject\",\"entries\":[\"domain_suffix:example.com\"]}]}");
    assert(!dns_answer(e,q,n,reply,sizeof(reply),&rn)&&(reply[3]&15)==5&&upstream_requests==1);
    policy(e,"{\"route_rules\":[{\"id\":\"proxy\",\"action\":\"probe_exit\",\"exit_node_id\":\"8\",\"entries\":[\"domain_suffix:example.com\"]}]}");
    assert(!dns_answer(e,q,n,reply,sizeof(reply),&rn)&&!vr_dns_address4(reply,rn,ip)&&!strcmp(ip,"198.18.0.20")&&mapping_requests==1);
    for(unsigned i=0;i<3;i++){int types[]={28,64,65};n=query(q,types[i]);assert(!dns_answer(e,q,n,reply,sizeof(reply),&rn)&&!(reply[3]&15)&&!reply[7]&&upstream_requests==1);}
    policy(e,"{\"route_rules\":[{\"id\":\"ip\",\"action\":\"reject\",\"entries\":[\"cidr:203.0.113.0/24\"]},{\"id\":\"direct\",\"action\":\"direct\",\"entries\":[\"domain_suffix:example.com\"]}]}");
    n=query(q,28);assert(!dns_answer(e,q,n,reply,sizeof(reply),&rn)&&(reply[3]&15)==5&&upstream_requests==2);
    n=query(q,1);assert(!dns_answer(e,q,n,reply,sizeof(reply),&rn)&&(reply[3]&15)==5&&upstream_requests==3);
    policy(e,"{\"route_rules\":[]}");assert(!vr_dns_start(e));
    for(int tcp=0;tcp<2;tcp++){
        int client=socket(AF_INET,tcp?SOCK_STREAM:SOCK_DGRAM,0);assert(client>=0);struct sockaddr_in a={0};a.sin_family=AF_INET;a.sin_port=htons(1053);inet_pton(AF_INET,"127.0.0.1",&a.sin_addr);assert(!connect(client,(struct sockaddr *)&a,sizeof(a)));
        struct timeval timeout={5,0};setsockopt(client,SOL_SOCKET,SO_RCVTIMEO,&timeout,sizeof(timeout));
        if(tcp){uint8_t h[2]={(uint8_t)(n>>8),(uint8_t)n};assert(send(client,h,2,0)==2);}
        assert(send(client,q,n,0)==(ssize_t)n);vr_dns_accept(e,tcp);
        if(tcp){uint8_t h[2];assert(!read_all(client,h,2));rn=(h[0]<<8)|h[1];assert(rn<sizeof(reply)&&!read_all(client,reply,rn));}
        else {ssize_t size=recv(client,reply,sizeof(reply),0);assert(size>0);rn=(size_t)size;}
        assert(!vr_dns_address4(reply,rn,ip)&&!strcmp(ip,"203.0.113.5"));close(client);
    }
    vr_dns_stop(e);pthread_cancel(thread);pthread_join(thread,NULL);close(fd);json_object_put(e->policy);pthread_cond_destroy(&e->jobs_changed);pthread_mutex_destroy(&e->mutex);free(e);
    puts("PASS: DNS direct/reject/fake-IP, IPv6/HTTPS leak prevention, rule precedence, TCP and UDP worker service");return 0;
}
