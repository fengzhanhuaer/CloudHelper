#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <arpa/inet.h>
#include <poll.h>
#include <signal.h>
#include <spawn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static volatile sig_atomic_t stopping;
static volatile sig_atomic_t toggle_tls;
static volatile sig_atomic_t refresh_addresses,fail_bind;
static volatile sig_atomic_t repair_firewall;
static int carrier=-1;static struct sockaddr_in peer;static socklen_t peer_len;
static void stop(int sig){(void)sig;stopping=1;}
static void toggle(int sig){(void)sig;toggle_tls=1;}
static void refresh(int sig){if(sig==SIGALRM)fail_bind=1;else refresh_addresses=1;}
static void repair(int sig){(void)sig;repair_firewall=1;}
int __real_posix_spawnp(pid_t *pid,const char *file,const posix_spawn_file_actions_t *actions,const posix_spawnattr_t *attr,char *const args[],char *const env[]);
int __wrap_posix_spawnp(pid_t *pid,const char *file,const posix_spawn_file_actions_t *actions,const posix_spawnattr_t *attr,char *const args[],char *const env[])
{
    const char *failure=getenv("CHVR_PLATFORM_FAIL_FILE");
    const char *check_failure=getenv("CHVR_CHECK_FAIL_FILE");
    if(check_failure&&!strcmp(file,"iptables")&&access(check_failure,F_OK)==0) {
        for(size_t i=1;args[i];i++)if(!strcmp(args[i],"-C"))return __real_posix_spawnp(pid,"sh",actions,attr,(char *const[]){"sh","-c","exit 4",NULL},env);
    }
    if(failure&&!strcmp(file,"iptables")&&args[1]&&!strcmp(args[1],"-t")&&args[3]&&!strcmp(args[3],"-D")&&args[4]&&!strcmp(args[4],"CHVR_NAT")&&access(failure,F_OK)==0) {
        unlink(failure);return __real_posix_spawnp(pid,"sh",actions,attr,(char *const[]){"sh","-c","exit 7",NULL},env);
    }
    return __real_posix_spawnp(pid,file,actions,attr,args,env);
}
int vr_path(vr_engine *e,const char *node,char path[VR_PATH_MAX][129],size_t *n)
{if(carrier<0)return -1;memset(path,0,VR_PATH_MAX*129);strcpy(path[0],e->config.node_id);strcpy(path[1],node);*n=2;return 0;}
int vr_send(vr_engine *e,uint16_t m,uint16_t s,char path[VR_PATH_MAX][129],size_t n,const void *b,size_t len)
{(void)e;(void)m;(void)s;(void)path;(void)n;return carrier<0?-1:(sendto(carrier,b,len,0,(struct sockaddr *)&peer,peer_len)==(ssize_t)len?0:-1);}
int vr_send_json(vr_engine *e,uint16_t m,uint16_t s,char path[VR_PATH_MAX][129],size_t n,json_object *o)
{const char *text=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);return vr_send(e,m,s,path,n,text,strlen(text));}

int main(int argc,char **argv)
{
    if(argc==3&&!strcmp(argv[1],"--dns"))return vr_platform_dns(atoi(argv[2]))?1:0;
    assert(argc==2);signal(SIGTERM,stop);signal(SIGINT,stop);signal(SIGPIPE,SIG_IGN);signal(SIGUSR1,toggle);signal(SIGUSR2,refresh);signal(SIGALRM,refresh);signal(SIGWINCH,repair);
    vr_service service={0};pthread_mutex_init(&service.mutex,NULL);
    vr_engine *e=calloc(1,sizeof(*e));assert(e);e->service=&service;pthread_mutex_init(&e->mutex,NULL);vr_config_default(&e->config);
    int gateway=!strncmp(argv[1],"gateway",7);
    int readdress=!strcmp(argv[1],"readdress");
    int routing=!strcmp(argv[1],"routing")||readdress;e->config.enabled=routing;
    strcpy(e->config.node_id,"8");e->config.allow_wan=atoi(argv[1]);
    if(readdress)e->config.allow_wan=e->config.tls_sni_enabled=1;
    if(gateway){e->config.gateway.enabled=1;e->config.gateway.cidr_count=1;e->config.gateway.node_count=1;strcpy(e->config.gateway.cidrs[0],"192.168.52.0/24");strcpy(e->config.gateway.nodes[0],"7");}
    if(!strcmp(argv[1],"gateway-disabled"))e->config.gateway.enabled=0;
    if(!strcmp(argv[1],"gateway-excluded"))strcpy(e->config.gateway.cidrs[0],"192.168.52.128/25");
    if(!strcmp(argv[1],"gateway-revoked"))strcpy(e->config.gateway.nodes[0],"9");
    strcpy(e->addresses.lan,"192.168.52.1");strcpy(e->addresses.lan_if,"chvrlan");
    strcpy(e->addresses.wan,"203.0.113.1");strcpy(e->addresses.wan_if,"chvrwan");
    strcpy(e->own_ip,"198.18.0.8");strcpy(e->cidr,"198.18.0.0/15");
    const char *policy="{\"probe_ips\":[{\"node_id\":\"8\",\"ip\":\"198.18.0.8\"},{\"node_id\":\"7\",\"ip\":\"198.18.0.7\"}]}";
    e->policy=vr_json_parse(policy,strlen(policy));assert(e->policy);
    if(routing) {
        const char *rules="[{\"id\":\"blocked-sni\",\"action\":\"reject\",\"entries\":[\"domain:blocked.example\"]},{\"id\":\"ordinary-cidr\",\"action\":\"probe_exit\",\"exit_node_id\":\"8\",\"entries\":[\"cidr:198.51.100.0/24\"]}]";
        json_object_object_add(e->policy,"route_rules",vr_json_parse(rules,strlen(rules)));
    }
    vr_mapping *m=&e->mappings[0];strcpy(m->domain,"203.0.113.5");strcpy(m->ip,"198.18.0.20");strcpy(m->exit,"8");m->expires=time(NULL)+120;
    e->lan=vr_bind4(e->addresses.lan,e->config.port,1);assert(e->lan>=0);
    e->wan=e->config.allow_wan?vr_bind4(e->addresses.wan,e->config.port,1):-1;
    assert(!e->config.allow_wan||e->wan>=0);
    e->tun=vr_platform_start(&e->config,&e->addresses,e->own_ip,e->cidr);assert(e->tun>=0&&vr_platform_healthy());
    if(routing)assert(!vr_platform_routes(e->policy)&&!vr_tcp_start(e));
    if(gateway){carrier=vr_bind4(e->addresses.wan,18082,0);assert(carrier>=0);}
    int streams[32];for(size_t i=0;i<32;i++)streams[i]=-1;
    puts("READY");fflush(stdout);
    while(!stopping){
        if(repair_firewall) {
            int tun=e->tun,lan=e->lan,wan=e->wan;unsigned generation=e->generation;char detail[240];
            int healthy=vr_platform_check(detail,sizeof(detail));
            if(!healthy&&getenv("CHVR_CHECK_FAIL_FILE")&&access(getenv("CHVR_CHECK_FAIL_FILE"),F_OK)==0)
                assert(strstr(detail,"SNI hook")&&strstr(detail,"exit 4"));
            int result=vr_platform_repair(&e->config,&e->addresses,e->cidr);
            assert(tun==e->tun&&lan==e->lan&&wan==e->wan&&generation==e->generation);
            puts(result?"REPAIR_DEFERRED":"REPAIRED");fflush(stdout);repair_firewall=0;
        }
        if(refresh_addresses||fail_bind) {
            vr_addresses next=e->addresses;
            if(fail_bind)strcpy(next.lan,"198.51.100.99");
            else {
                int original=!strcmp(next.lan,"192.168.52.1");
                strcpy(next.lan,original?"192.168.52.2":"192.168.52.1");strcpy(next.wan,original?"203.0.113.2":"203.0.113.1");
                strcpy(next.lan_if,original?"chvrlan2":"chvrlan");strcpy(next.wan_if,original?"chvrwan2":"chvrwan");
            }
            int tun=e->tun;unsigned generation=e->generation;
            int result=vr_addresses_apply(e,&next);assert(e->tun==tun&&e->generation==generation);
            puts(result?"REFRESH_FAILED":"UPDATED");fflush(stdout);refresh_addresses=fail_bind=0;
        }
        if(readdress) {
            int listeners[]={e->lan,e->wan};
            for(size_t i=0;i<2;i++) {int fd=accept(listeners[i],NULL,NULL);if(fd>=0) {size_t j;for(j=0;j<32&&streams[j]>=0;j++);assert(j<32);streams[j]=fd;}}
            for(size_t i=0;i<32;i++)if(streams[i]>=0) {
                struct pollfd fd={streams[i],POLLIN,0};if(poll(&fd,1,0)>0) {char text[256];ssize_t n=read(streams[i],text,sizeof(text));
                    if(n>0)assert(write(streams[i],text,(size_t)n)==n);else {close(streams[i]);streams[i]=-1;}}
            }
        }
        if(toggle_tls) {toggle_tls=0;e->config.tls_sni_enabled=!e->config.tls_sni_enabled;assert(!vr_platform_tls(&e->addresses,e->cidr,e->config.tls_sni_enabled));puts(e->config.tls_sni_enabled?"TLS_ON":"TLS_OFF");fflush(stdout);}
        struct pollfd p[131]={{e->tun,POLLIN,0},{carrier,POLLIN,0}};size_t count=vr_tcp_pollfds(e,p+2);if(poll(p,2+count,50)<0)continue;
        uint8_t b[2048];ssize_t n;
        if(p[0].revents&POLLIN){n=read(e->tun,b,sizeof(b));if(n>0)vr_outgoing(e,b,(size_t)n);}
        if(p[1].revents&POLLIN){peer_len=sizeof(peer);n=recvfrom(carrier,b,sizeof(b),0,(struct sockaddr *)&peer,&peer_len);if(n>1){char path[VR_PATH_MAX][129]={{"7"},{"8"}};if(b[0]!=7)strcpy(path[0],"9");vr_frame f={1,1,NULL,b+1,0,(size_t)n-1};vr_incoming(e,&f,path,2);}}
        if(count)vr_tcp_poll(e,p+2);
    }
    if(carrier>=0)close(carrier);
    for(size_t i=0;i<32;i++)if(streams[i]>=0)close(streams[i]);
    vr_tcp_stop(e);vr_platform_stop(e->cidr);close(e->tun);close(e->lan);if(e->wan>=0)close(e->wan);
    json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);return 0;
}
