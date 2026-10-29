#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

static vr_engine *entry,*exit_node;
static vr_service services[2];
static int listener,target_port,connections;
static char opened_id[33];
static void *echo(void *unused)
{
    (void)unused;
    for(;;) {int fd=accept(listener,NULL,NULL);if(fd<0)break;uint8_t data[65536];ssize_t n;
        while((n=read(fd,data,sizeof(data)))>0) {ssize_t at=0;while(at<n){ssize_t k=send(fd,data+at,(size_t)(n-at),MSG_NOSIGNAL);if(k<=0)break;at+=k;}if(at<n)break;}close(fd);}
    return NULL;
}
int __real_connect(int fd,const struct sockaddr *address,socklen_t n);
int __wrap_connect(int fd,const struct sockaddr *address,socklen_t n)
{
    assert(n==sizeof(struct sockaddr_in));struct sockaddr_in a=*(const struct sockaddr_in *)address;
    assert(a.sin_addr.s_addr==inet_addr("203.0.113.5"));connections++;
    a.sin_addr.s_addr=inet_addr("127.0.0.1");a.sin_port=htons((uint16_t)target_port);
    return __real_connect(fd,(const struct sockaddr *)&a,n);
}
int __wrap_vr_resolve_config4(const char *host,const vr_config *c,const char *iface,char out[16])
{(void)c;(void)iface;assert(!strcmp(host,"fixture.example"));usleep(50000);strcpy(out,"203.0.113.5");return 0;}
int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n)
{strcpy(path[0],e->config.node_id);strcpy(path[1],target);*n=2;return 0;}
int vr_send(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t count,const void *data,size_t n)
{assert(main==8);if(e==entry&&sub==1){json_object *o=vr_json_parse(data,n);strcpy(opened_id,vr_string(o,"session_id"));json_object_put(o);}vr_frame f={main,sub,NULL,data,0,n};vr_tcp_frame(e==entry?exit_node:entry,&f,path,count);return 0;}
int vr_send_json(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t count,json_object *o)
{const char *s=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);return vr_send(e,main,sub,path,count,s,strlen(s));}
int vr_bind4(const char *ip,int port,int tcp){(void)ip;(void)port;(void)tcp;return -1;}
static void tick(vr_engine *e)
{struct pollfd fds[129];size_t n=vr_tcp_pollfds(e,fds);if(n){assert(poll(fds,n,1)>=0);vr_tcp_poll(e,fds);}}
static size_t hello(uint8_t out[1024],const char *name)
{
    memset(out,0,1024);out[0]=22;out[1]=3;out[2]=1;out[5]=1;out[9]=3;out[10]=3;
    size_t p=43;out[p++]=0;out[p++]=0;out[p++]=2;out[p++]=0x13;out[p++]=1;out[p++]=1;out[p++]=0;
    size_t ext=9+strlen(name);out[p++]=(uint8_t)(ext>>8);out[p++]=(uint8_t)ext;
    out[p++]=0;out[p++]=0;ext=5+strlen(name);out[p++]=(uint8_t)(ext>>8);out[p++]=(uint8_t)ext;
    ext=3+strlen(name);out[p++]=(uint8_t)(ext>>8);out[p++]=(uint8_t)ext;out[p++]=0;
    ext=strlen(name);out[p++]=(uint8_t)(ext>>8);out[p++]=(uint8_t)ext;memcpy(out+p,name,ext);p+=ext;
    out[3]=(uint8_t)((p-5)>>8);out[4]=(uint8_t)(p-5);out[6]=(uint8_t)((p-9)>>16);out[7]=(uint8_t)((p-9)>>8);out[8]=(uint8_t)(p-9);return p;
}
static void policy(vr_engine *e,const char *action)
{
    char text[512];snprintf(text,sizeof(text),"{\"route_rules\":[{\"id\":\"sni-rule\",\"enabled\":true,\"action\":\"%s\",\"exit_node_id\":\"8\",\"entries\":[\"domain:fixture.example\"]}]}",action);
    json_object_put(e->policy);e->policy=vr_json_parse(text,strlen(text));assert(e->policy);
}
static vr_engine *engine(const char *id)
{vr_engine *e=calloc(1,sizeof(*e));assert(e);e->service=&services[!strcmp(id,"8")];pthread_mutex_init(&e->service->mutex,NULL);vr_config_default(&e->config);strcpy(e->config.node_id,id);pthread_mutex_init(&e->mutex,NULL);policy(e,"probe_exit");return e;}
static void parser_tests(void)
{
    uint8_t wire[1024],split[1100];char name[254];size_t n=hello(wire,"FiXtUrE.Example");
    for(size_t i=0;i<n;i++){assert(!vr_tls_server_name(wire,i,name));assert(!*name);}
    assert(vr_tls_server_name(wire,n,name)&&!strcmp(name,"fixture.example"));
    size_t k=21;memcpy(split,wire,5);split[3]=0;split[4]=(uint8_t)k;memcpy(split+5,wire+5,k);
    memcpy(split+5+k,wire,5);split[8+k]=(uint8_t)((n-5-k)>>8);split[9+k]=(uint8_t)(n-5-k);memcpy(split+10+k,wire+5+k,n-5-k);
    assert(vr_tls_server_name(split,n+5,name)&&!strcmp(name,"fixture.example"));
    const char *invalid[]={"a..example","-bad.example","bad-.example","bad/example","1.2.3.4"};
    for(size_t i=0;i<sizeof(invalid)/sizeof(*invalid);i++){n=hello(wire,invalid[i]);assert(vr_tls_server_name(wire,n,name));assert(!*name);}
    n=hello(wire,"fixture.example");wire[4]=255;assert(!vr_tls_server_name(wire,n,name));
    wire[3]=255;wire[4]=255;assert(vr_tls_server_name(wire,n,name)&&!*name);
    for(size_t i=0;i<65536;i++){size_t len=i%sizeof(wire);for(size_t j=0;j<len;j++)wire[j]=(uint8_t)(i*33+j*17);vr_tls_server_name(wire,len,name);}
    puts("PASS: complete/partial/multi-record ClientHello, malformed lengths, names, bounded parser inputs");
}
static void transfer(const char *action)
{
    policy(entry,action);policy(exit_node,"reject");int pair[2];assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_NONBLOCK,0,pair));
    assert(!vr_tcp_attach(entry,pair[1],"203.0.113.5",443));uint8_t wire[1024],reply[1024];size_t n=hello(wire,"fixture.example");
    int before=connections;assert(write(pair[0],wire,3)==3);for(int i=0;i<3;i++){tick(entry);tick(exit_node);}assert(connections==before);
    assert(write(pair[0],wire+3,n-3)==(ssize_t)n-3);size_t got=0;uint64_t deadline=vr_millis()+5000;
    while(got<n&&vr_millis()<deadline) {tick(entry);tick(exit_node);ssize_t k=read(pair[0],reply+got,n-got);if(k>0)got+=(size_t)k;else if(!k)break;}
    if(!strcmp(action,"reject")){assert(!got&&connections==before);}
    else {
        assert(got==n&&!memcmp(wire,reply,n)&&connections==before+1);
        if(!strcmp(action,"probe_exit")) {
            char data[128];snprintf(data,sizeof(data),"{\"session_id\":\"%s\"}",opened_id);
            char wrong[VR_PATH_MAX][129]={{"9"},{"7"}};vr_frame f={8,4,NULL,(const uint8_t *)data,0,strlen(data)};
            vr_tcp_frame(entry,&f,wrong,2);
        }
        /* Policy refresh changes new decisions without breaking this stream. */
        policy(entry,"reject");assert(write(pair[0],"still-connected",15)==15);got=0;deadline=vr_millis()+3000;
        while(got<15&&vr_millis()<deadline){tick(entry);tick(exit_node);ssize_t k=read(pair[0],reply+got,15-got);if(k>0)got+=(size_t)k;}
        assert(got==15&&!memcmp(reply,"still-connected",15));
    }
    close(pair[0]);for(int i=0;i<8;i++){tick(entry);tick(exit_node);}vr_tcp_stop(entry);vr_tcp_stop(exit_node);
}
int main(void)
{
    parser_tests();entry=engine("7");exit_node=engine("8");
    listener=socket(AF_INET,SOCK_STREAM,0);assert(listener>=0);struct sockaddr_in a={0};a.sin_family=AF_INET;a.sin_addr.s_addr=inet_addr("127.0.0.1");assert(!bind(listener,(struct sockaddr *)&a,sizeof(a)));assert(!listen(listener,8));socklen_t size=sizeof(a);assert(!getsockname(listener,(struct sockaddr *)&a,&size));target_port=ntohs(a.sin_port);
    pthread_t thread;assert(!pthread_create(&thread,NULL,echo,NULL));
    transfer("direct");transfer("probe_exit");transfer("reject");
    int before=connections;char path[VR_PATH_MAX][129]={{"7"},{"8"}};
    const char *denied[]={"127.0.0.1:22","169.254.169.254:80","192.168.1.1:80"};
    policy(exit_node,"direct");
    for(size_t i=0;i<sizeof(denied)/sizeof(denied[0]);i++){char data[512];snprintf(data,sizeof(data),"{\"session_id\":\"0123456789abcdef0123456789abcdef\",\"source_node_id\":\"7\",\"exit_node_id\":\"8\",\"target_addr\":\"%s\"}",denied[i]);vr_frame f={8,1,NULL,(const uint8_t *)data,0,strlen(data)};vr_tcp_frame(exit_node,&f,path,2);}
    assert(connections==before);vr_tcp_stop(exit_node);
    int held[64];
    for(size_t i=0;i<64;i++) {int pair[2];assert(!socketpair(AF_UNIX,SOCK_STREAM,0,pair));assert(!vr_tcp_attach(entry,pair[1],"203.0.113.5",443));held[i]=pair[0];}
    int full[2];assert(!socketpair(AF_UNIX,SOCK_STREAM,0,full));assert(vr_tcp_attach(entry,full[1],"203.0.113.5",443));close(full[0]);close(full[1]);
    vr_tcp_stop(entry);for(size_t i=0;i<64;i++)close(held[i]);
    shutdown(listener,SHUT_RDWR);close(listener);pthread_join(thread,NULL);
    json_object_put(entry->policy);json_object_put(exit_node->policy);pthread_mutex_destroy(&entry->mutex);pthread_mutex_destroy(&exit_node->mutex);free(entry);free(exit_node);
    puts("PASS: fragmented SNI direct/reject/remote TCP, asynchronous exit DNS, retained stream, generic authenticated exit, private targets denied");return 0;
}
