#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <arpa/inet.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static volatile sig_atomic_t stopping;
static int carrier=-1;static struct sockaddr_in peer;static socklen_t peer_len;
static void stop(int sig){(void)sig;stopping=1;}
int vr_path(vr_engine *e,const char *node,char path[VR_PATH_MAX][129],size_t *n)
{if(carrier<0)return -1;memset(path,0,VR_PATH_MAX*129);strcpy(path[0],e->config.node_id);strcpy(path[1],node);*n=2;return 0;}
int vr_send(vr_engine *e,uint16_t m,uint16_t s,char path[VR_PATH_MAX][129],size_t n,const void *b,size_t len)
{(void)e;(void)m;(void)s;(void)path;(void)n;return carrier<0?-1:(sendto(carrier,b,len,0,(struct sockaddr *)&peer,peer_len)==(ssize_t)len?0:-1);}

int main(int argc,char **argv)
{
    if(argc==3&&!strcmp(argv[1],"--dns"))return vr_platform_dns(atoi(argv[2]))?1:0;
    assert(argc==2);signal(SIGTERM,stop);signal(SIGINT,stop);signal(SIGPIPE,SIG_IGN);
    vr_engine *e=calloc(1,sizeof(*e));assert(e);pthread_mutex_init(&e->mutex,NULL);vr_config_default(&e->config);
    int gateway=!strncmp(argv[1],"gateway",7);
    strcpy(e->config.node_id,"8");e->config.allow_wan=atoi(argv[1]);
    if(gateway){e->config.gateway.enabled=1;e->config.gateway.cidr_count=1;e->config.gateway.node_count=1;strcpy(e->config.gateway.cidrs[0],"192.168.52.0/24");strcpy(e->config.gateway.nodes[0],"7");}
    if(!strcmp(argv[1],"gateway-disabled"))e->config.gateway.enabled=0;
    if(!strcmp(argv[1],"gateway-excluded"))strcpy(e->config.gateway.cidrs[0],"192.168.52.128/25");
    if(!strcmp(argv[1],"gateway-revoked"))strcpy(e->config.gateway.nodes[0],"9");
    strcpy(e->addresses.lan,"192.168.52.1");strcpy(e->addresses.lan_if,"chvrlan");
    strcpy(e->addresses.wan,"203.0.113.1");strcpy(e->addresses.wan_if,"chvrwan");
    strcpy(e->own_ip,"198.18.0.8");strcpy(e->cidr,"198.18.0.0/15");
    const char *policy="{\"probe_ips\":[{\"node_id\":\"8\",\"ip\":\"198.18.0.8\"},{\"node_id\":\"7\",\"ip\":\"198.18.0.7\"}]}";
    e->policy=vr_json_parse(policy,strlen(policy));assert(e->policy);
    vr_mapping *m=&e->mappings[0];strcpy(m->domain,"203.0.113.5");strcpy(m->ip,"198.18.0.20");strcpy(m->exit,"8");m->expires=time(NULL)+120;
    e->lan=vr_bind4(e->addresses.lan,e->config.port,1);assert(e->lan>=0);
    e->wan=e->config.allow_wan?vr_bind4(e->addresses.wan,e->config.port,1):-1;
    assert(!e->config.allow_wan||e->wan>=0);
    e->tun=vr_platform_start(&e->config,&e->addresses,e->own_ip,e->cidr);assert(e->tun>=0&&vr_platform_healthy());
    if(gateway){carrier=vr_bind4(e->addresses.wan,18082,0);assert(carrier>=0);}
    puts("READY");fflush(stdout);
    while(!stopping){struct pollfd p[2]={{e->tun,POLLIN,0},{carrier,POLLIN,0}};if(poll(p,2,50)<=0)continue;
        uint8_t b[2048];ssize_t n;
        if(p[0].revents&POLLIN){n=read(e->tun,b,sizeof(b));if(n>0)vr_outgoing(e,b,(size_t)n);}
        if(p[1].revents&POLLIN){peer_len=sizeof(peer);n=recvfrom(carrier,b,sizeof(b),0,(struct sockaddr *)&peer,&peer_len);if(n>1){char path[VR_PATH_MAX][129]={{"7"},{"8"}};if(b[0]!=7)strcpy(path[0],"9");vr_frame f={1,1,NULL,b+1,0,(size_t)n-1};vr_incoming(e,&f,path,2);}}
    }
    if(carrier>=0)close(carrier);
    vr_platform_stop(e->cidr);close(e->tun);close(e->lan);if(e->wan>=0)close(e->wan);
    json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);return 0;
}
