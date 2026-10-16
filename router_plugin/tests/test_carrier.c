#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>
#include <openssl/err.h>

void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count)
{vr_measure_frame(l->engine,l,f,path,count);}

static void tick(vr_engine *e)
{
    vr_handshakes_poll(e,0);vr_link *l=&e->links[0];
    if(e->lan>=0){int fd=accept4(e->lan,NULL,NULL,SOCK_CLOEXEC);if(fd>=0)vr_link_accept(e,fd);}
    if(!l->ssl){vr_link_dial(l);poll(NULL,0,10);return;}
    struct pollfd fd={l->fd,(short)(POLLIN|(l->used?POLLOUT:0)),0};assert(poll(&fd,1,10)>=0);
    /* Certificate parsing/curl can leave a thread-local OpenSSL error. A
       nonblocking WANT_READ/WANT_WRITE must not misclassify that old error. */
    ERR_put_error(ERR_LIB_PEM,0,PEM_R_NO_START_LINE,__FILE__,__LINE__);
    vr_link_poll(l,fd.revents);vr_measure_tick(e);
}

int main(int argc,char **argv)
{
    assert(argc==2);signal(SIGPIPE,SIG_IGN);
    vr_engine *e=calloc(1,sizeof(*e));assert(e);vr_service s={0};e->service=&s;e->lan=-1;
    pthread_mutex_init(&e->mutex,NULL);pthread_mutex_init(&s.mutex,NULL);vr_config_default(&e->config);strcpy(e->config.node_id,"7");
    char buffer[32768];size_t len;assert(!vr_read_file(argv[1],buffer,sizeof(buffer),&len));e->policy=vr_json_parse(buffer,len);assert(e->policy);
    e->link_count=1;vr_link *l=&e->links[0];l->engine=e;l->fd=-1;l->rule=json_object_array_get_idx(vr_member(e->policy,"topology_rules"),0);
    assert(!vr_route_id(vr_string(l->rule,"id"),l->route));strcpy(l->peer,"8");
    if(json_object_get_boolean(vr_member(e->policy,"test_server"))) {
        e->tls=vr_tls_server_context(vr_string(e->policy,"test_cert"),vr_string(e->policy,"test_key"));assert(e->tls);
        e->nonces=json_object_new_object();assert(!vr_copy(e->replay_path,sizeof(e->replay_path),argv[1]));
        char *slash=strrchr(e->replay_path,'/');assert(slash);strcpy(slash,"/replay.json");
        e->lan=vr_bind4("127.0.0.1",json_object_get_int(vr_member(e->policy,"test_port")),1);assert(e->lan>=0);
        puts("READY");fflush(stdout);
    }
    uint64_t deadline=vr_millis()+10000;
    while(!l->ssl&&vr_millis()<deadline)tick(e);
    if(!l->ssl)fprintf(stderr,"carrier: %s\n",l->error);
    assert(l->ssl);
    int small_buffer=4096;assert(!setsockopt(l->fd,SOL_SOCKET,SO_SNDBUF,&small_buffer,sizeof(small_buffer)));
    const char *kinds[]={"ping","upload","download"};
    for(unsigned i=0;i<3;i++) {
        strcpy(s.measure_node,"8");strcpy(s.measure_kind,kinds[i]);json_object_put(s.measurement);s.measurement=json_object_new_object();
        json_object_object_add(s.measurement,"running",json_object_new_boolean(1));json_object_object_add(s.measurement,"kind",json_object_new_string(kinds[i]));
        deadline=vr_millis()+15000;
        while(json_object_get_boolean(vr_member(s.measurement,"running"))&&vr_millis()<deadline)tick(e);
        json_object *result=vr_member(s.measurement,"result");
        if(!result||!json_object_get_boolean(vr_member(result,"ok")))fprintf(stderr,"measurement: %s\n",json_object_to_json_string(s.measurement));
        assert(result&&json_object_get_boolean(vr_member(result,"ok")));
        if(i){int64_t bytes=json_object_get_int64(vr_member(result,"bytes"));
            if(bytes<=0||bytes>16*1024*1024)fprintf(stderr,"measurement bounds: %s\n",json_object_to_json_string(result));
            assert(bytes>0&&bytes<=16*1024*1024);}
        printf("PASS: C to existing Probe %s\n",kinds[i]);fflush(stdout);
    }
    vr_handshakes_poll(e,1);vr_link_close(l,"");json_object_put(e->policy);json_object_put(s.measurement);
    for(size_t i=0;i<4;i++)json_object_put(e->receives[i].message);
    if(e->lan>=0)close(e->lan);
    SSL_CTX_free(e->tls);json_object_put(e->nonces);
    json_object_put(e->speed_send);pthread_mutex_destroy(&s.mutex);pthread_mutex_destroy(&e->mutex);free(e);return 0;
}
