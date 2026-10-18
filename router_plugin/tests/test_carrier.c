#define _GNU_SOURCE
#include "engine.h"
#include <assert.h>
#include <poll.h>
#include <netinet/tcp.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>
#include <openssl/err.h>

static unsigned delivered,received_slots;
void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count)
{delivered++;if(f->main==99){strcpy(path[0],"dispatch-mutated-path");return;}
 if(f->main==2&&f->sub==1)received_slots|=1u<<l->slot;
 vr_measure_frame(l->engine,l,f,path,count);}

static void test_ingress(vr_engine *e,vr_link *l)
{
    const char *controls[]={"{", "{\"path\":[\"9\",\"7\"]}",
        "{\"path\":[\"9\",\"8\",\"7\"]}", "{\"path\":[\"8\",\"7\",\"8\"]}",
        "{\"path\":[\"1\",\"2\",\"3\",\"8\",\"7\"]}",
        "{\"path\":[\"8\",\"7\"]}", "{}", "{\"path\":[]}", "{\"path\":null}", ""};
    unsigned before=delivered;uint8_t bytes[256];size_t n;vr_decoder decoder={0};
    for(size_t i=0;i<sizeof(controls)/sizeof(*controls);i++) {
        vr_frame frame={99,0,(const uint8_t *)controls[i],(const uint8_t *)"{}",strlen(controls[i]),2};
        assert(!vr_frame_encode(&frame,bytes,sizeof(bytes),&n));
        /* Bad business frames must not poison subsequent frames on the same
           decoder/carrier; authorization is still enforced for every frame. */
        assert(!vr_decoder_feed(&decoder,bytes,n,vr_link_receive,l));
        assert(delivered==before+(i<5?0:(unsigned)i-4));
    }
    /* A cached path cannot authorize a different frame; equality includes all
       control bytes. Dispatch may mutate its path (ping reverses it). */
    const char *valid="{\"path\":[\"8\",\"7\"]}";
    vr_frame frame={99,0,(const uint8_t *)valid,(const uint8_t *)"{}",strlen(valid),2};
    assert(!vr_link_receive(&frame,l));unsigned cached=delivered;
    assert(l->ingress_count==2&&!strcmp(l->ingress_path[0],"8"));
    assert(!vr_link_receive(&frame,l));assert(delivered==cached+1&&!strcmp(l->ingress_path[0],"8"));
    const char *forged="{\"path\":[\"9\",\"7\"]}";frame.control=(const uint8_t *)forged;
    assert(!vr_link_receive(&frame,l));assert(delivered==cached+1);
    vr_link_close(l,"");assert(!l->ingress_count);
    assert(!e->links[0].ssl);fputs("PASS: Probe ingress fallback, authorization and frame-local rejection\n",stderr);
}

static void tick(vr_engine *e)
{
    vr_handshakes_poll(e,0);
    if(e->lan>=0){int fd=accept4(e->lan,NULL,NULL,SOCK_CLOEXEC);if(fd>=0)vr_link_accept(e,fd);}
    struct pollfd fds[VR_CARRIER_MAX];
    for(size_t i=0;i<e->link_count;i++) {
        vr_link *l=&e->links[i];if(!l->ssl)vr_link_dial(l);
        fds[i]=(struct pollfd){l->fd,(short)(POLLIN|(l->used?POLLOUT:0)),0};
    }
    assert(poll(fds,e->link_count,10)>=0);
    /* Certificate parsing/curl can leave a thread-local OpenSSL error. A
       nonblocking WANT_READ/WANT_WRITE must not misclassify that old error. */
    ERR_put_error(ERR_LIB_PEM,0,PEM_R_NO_START_LINE,__FILE__,__LINE__);
    for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl)vr_link_poll(&e->links[i],fds[i].revents);
    vr_measure_tick(e);
}

static int active(vr_engine *e)
{int count=0;for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl)count++;return count;}

static void test_pool(vr_engine *e)
{
    assert(vr_carrier_slot("GET / HTTP/1.1\r\n\r\n")==0);
    assert(vr_carrier_slot("GET / HTTP/1.1\r\nX-Codex-VRouter-Carrier-Slot: \r\n\r\n")==0);
    assert(vr_carrier_slot("GET / HTTP/1.1\r\nx-codex-vrouter-carrier-slot: 3\r\n\r\n")==3);
    const char *invalid[]={"-1","4","264","oops","1oops","99999999999999999999999999999999999999999","0\r\nX-Codex-VRouter-Carrier-Slot: 1"};
    for(size_t i=0;i<sizeof(invalid)/sizeof(*invalid);i++) {
        char header[256];snprintf(header,sizeof(header),"GET / HTTP/1.1\r\nX-Codex-VRouter-Carrier-Slot: %s\r\n\r\n",invalid[i]);
        assert(vr_carrier_slot(header)<0);
    }
    /* Use real SSL objects, without a network, to exercise the production
       selection/queue/status paths and prove they remain bounded. */
    SSL_CTX *ctx=SSL_CTX_new(TLS_method());assert(ctx);
    for(size_t i=0;i<e->link_count;i++){e->links[i].ssl=SSL_new(ctx);assert(e->links[i].ssl);e->links[i].session=++e->carrier_session;}
    uint8_t packet[28]={0x45,0,0,28,0,0,0,0,64,17,0,0,10,0,0,1,10,0,0,2,0,0,1,187,0,8,0,0};
    uint8_t reverse[28];vr_frame f={1,1,NULL,packet,0,sizeof(packet)};
    unsigned seen=0;vr_link *first=NULL;
    for(unsigned port=1000;port<1032;port++) {
        packet[20]=(uint8_t)(port>>8);packet[21]=(uint8_t)port;
        vr_link *l=vr_carrier_select(e,"8",&f);assert(l);seen|=1u<<l->slot;if(!first)first=l;
        printf("FLOW %u %d\n",port,l->slot);
        memcpy(reverse,packet,sizeof(packet));memcpy(reverse+12,packet+16,4);memcpy(reverse+16,packet+12,4);
        memcpy(reverse+20,packet+22,2);memcpy(reverse+22,packet+20,2);f.data=reverse;
        assert(vr_carrier_select(e,"8",&f)==l);f.data=packet;
    }
    assert(seen==15);
    vr_link *selected=vr_carrier_select(e,"8",&f),*other=&e->links[(selected->slot+1)%4];
    uint64_t other_session=other->session;
    vr_link_close(other,"");assert(vr_carrier_select(e,"8",&f)==selected);
    other->ssl=SSL_new(ctx);other->session=++e->carrier_session;assert(other->session!=other_session);
    assert(vr_carrier_select(e,"8",&f)==selected); /* recovered slots don't steal flows */
    char path[VR_PATH_MAX][129]={{"7"},{"8"}};
    selected->used=VR_TX_CAP;assert(vr_send(e,1,1,path,2,packet,sizeof(packet))<0);
    assert(vr_carrier_select(e,"8",&f)==selected);selected->used=0; /* full queues cannot reorder onto another slot */
    vr_link_close(selected,"");vr_link *fallback=vr_carrier_select(e,"8",&f);assert(fallback&&fallback!=selected);
    selected->ssl=SSL_new(ctx);selected->session=++e->carrier_session;
    assert(vr_carrier_select(e,"8",&f)==fallback);
    const char *open="{\"session_id\":\"00112233445566778899AABBCCDDEEFF\"}";
    vr_frame proxy={8,1,NULL,(const uint8_t *)open,0,strlen(open)};
    vr_link *bound=vr_carrier_select(e,"8",&proxy);
    uint8_t data[17]={0x00,0x11,0x22,0x33,0x44,0x55,0x66,0x77,0x88,0x99,0xaa,0xbb,0xcc,0xdd,0xee,0xff,1};
    proxy.sub=3;proxy.data=data;proxy.data_len=sizeof(data);assert(vr_carrier_select(e,"8",&proxy)==bound);
    json_object *rows=vr_links_status(e);assert(json_object_array_length(rows)==1);
    assert(json_object_get_int(vr_member(json_object_array_get_idx(rows,0),"carrier_count"))==4);json_object_put(rows);
    for(size_t i=0;i<e->link_count;i++){vr_link_close(&e->links[i],"");e->links[i].retry=0;}
    memset(e->bindings,0,sizeof(e->bindings));SSL_CTX_free(ctx);
    fputs("PASS: flow affinity, symmetric keys, recovery, backpressure and bounded status\n",stderr);
}

int main(int argc,char **argv)
{
    assert(argc==2);signal(SIGPIPE,SIG_IGN);
    vr_engine *e=calloc(1,sizeof(*e));assert(e);vr_service s={0};e->service=&s;e->lan=-1;
    pthread_mutex_init(&e->mutex,NULL);pthread_mutex_init(&s.mutex,NULL);vr_config_default(&e->config);strcpy(e->config.node_id,"7");
    char buffer[32768];size_t len;assert(!vr_read_file(argv[1],buffer,sizeof(buffer),&len));e->policy=vr_json_parse(buffer,len);assert(e->policy);
    assert(!vr_links_add(e,json_object_array_get_idx(vr_member(e->policy,"topology_rules"),0)));vr_link *l=&e->links[0];
    test_ingress(e,l);
    test_pool(e);
    if(json_object_get_boolean(vr_member(e->policy,"test_server"))) {
        e->tls=vr_tls_server_context(vr_string(e->policy,"test_cert"),vr_string(e->policy,"test_key"));assert(e->tls);
        e->nonces=json_object_new_object();assert(!vr_copy(e->replay_path,sizeof(e->replay_path),argv[1]));
        char *slash=strrchr(e->replay_path,'/');assert(slash);strcpy(slash,"/replay.json");
        e->lan=vr_bind4("127.0.0.1",json_object_get_int(vr_member(e->policy,"test_port")),1);assert(e->lan>=0);
        puts("READY");fflush(stdout);
    }
    int expected=json_object_get_int(vr_member(e->policy,"test_expected_slots"));if(!expected)expected=4;
    uint64_t deadline=vr_millis()+15000;
    while(active(e)!=expected&&vr_millis()<deadline)tick(e);
    if(!l->ssl)fprintf(stderr,"carrier: %s\n",l->error);
    assert(l->ssl&&active(e)==expected);
    deadline=vr_millis()+5000;while(received_slots!=(unsigned)((1<<expected)-1)&&vr_millis()<deadline)tick(e);
    assert(received_slots==(unsigned)((1<<expected)-1));
    puts("PASS: Probe VR frames received on every physical slot");fflush(stdout);
    for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl)assert(!vr_link_queue(&e->links[i],9,"slot-alive",10));
    deadline=vr_millis()+1000;while(vr_millis()<deadline)tick(e);
    if(expected==1)assert(active(e)==1); /* No speculative extra dials to legacy peers. */
    if(expected>1) {
        uint64_t sessions[4];for(int i=0;i<4;i++)sessions[i]=e->links[i].session;
        vr_link_close(&e->links[2],"test detach");e->links[2].retry=0;
        assert(active(e)==3);puts("DETACHED 2");fflush(stdout);
        deadline=vr_millis()+15000;while(active(e)!=4&&vr_millis()<deadline)tick(e);
        assert(active(e)==4&&e->links[2].session!=sessions[2]);
        for(int i=0;i<4;i++)if(i!=2)assert(e->links[i].session==sessions[i]);
        puts("PASS: four carriers, isolated detach and reconnect");fflush(stdout);
    }
    int no_delay=0;socklen_t no_delay_length=sizeof(no_delay);
    assert(!getsockopt(l->fd,IPPROTO_TCP,TCP_NODELAY,&no_delay,&no_delay_length)&&no_delay==1);
    int small_buffer=4096;assert(!setsockopt(l->fd,SOL_SOCKET,SO_SNDBUF,&small_buffer,sizeof(small_buffer)));
    const char *kinds[]={"ping","upload","download"};
    for(unsigned i=0;i<3;i++) {
        strcpy(s.measure_node,"8");strcpy(s.measure_kind,kinds[i]);json_object_put(s.measurement);s.measurement=json_object_new_object();
        json_object_object_add(s.measurement,"running",json_object_new_boolean(1));json_object_object_add(s.measurement,"kind",json_object_new_string(kinds[i]));
        deadline=vr_millis()+VR_SPEED_TIMEOUT_MS+5000;
        while(json_object_get_boolean(vr_member(s.measurement,"running"))&&vr_millis()<deadline)tick(e);
        json_object *result=vr_member(s.measurement,"result");
        if(!result||!json_object_get_boolean(vr_member(result,"ok")))fprintf(stderr,"measurement: %s\n",json_object_to_json_string(s.measurement));
        assert(result&&json_object_get_boolean(vr_member(result,"ok")));
        if(i){int64_t bytes=json_object_get_int64(vr_member(result,"bytes"));
            if(bytes<=0||bytes>VR_SPEED_BYTES)fprintf(stderr,"measurement bounds: %s\n",json_object_to_json_string(result));
            assert(bytes>0&&bytes<=VR_SPEED_BYTES);}
        printf("PASS: C to existing Probe %s\n",kinds[i]);fflush(stdout);
    }
    vr_handshakes_poll(e,1);for(size_t i=0;i<e->link_count;i++)vr_link_close(&e->links[i],"");json_object_put(e->policy);json_object_put(s.measurement);
    for(size_t i=0;i<4;i++)json_object_put(e->receives[i].message);
    if(e->lan>=0)close(e->lan);
    SSL_CTX_free(e->tls);json_object_put(e->nonces);
    json_object_put(e->speed_send);pthread_mutex_destroy(&s.mutex);pthread_mutex_destroy(&e->mutex);free(e);return 0;
}
