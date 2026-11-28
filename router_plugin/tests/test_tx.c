#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <assert.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>
#include <openssl/pem.h>
#include <openssl/sha.h>

/* Exercise the actual frame encoder, scheduler, retry and decoder. Inject TLS
   backpressure without relying on timing or the host kernel's buffer sizes. */
static unsigned calls,want,frames,mains[4096],subs[4096],seqs[4096];
static const void *retry_bytes;
static size_t retry_n;
static uint8_t retry_copy[VR_TX_WRITE_MAX];
static vr_decoder decoder;
static unsigned ssl_error=SSL_ERROR_WANT_WRITE;
static int real_tls;
static SSL *busy_reader;
static unsigned busy_reads, frames_before_read;
int __real_SSL_read(SSL *ssl,void *p,int n);
int __real_SSL_write(SSL *ssl,const void *p,int n);
int __real_SSL_get_error(const SSL *ssl,int result);

void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{(void)l;(void)f;(void)path;(void)n;}

static int receive_frame(const vr_frame *f,void *arg)
{
    (void)arg;if(!f->main){assert(f->sub==VR_KEEPALIVE_PING&&!f->control_len&&!f->data_len);return 0;}assert(frames<4096);mains[frames]=f->main;subs[frames]=f->sub;
    seqs[frames]=f->data_len?f->data[0]:0;
    for(size_t i=0;i<f->data_len;i++)assert(f->data[i]==(uint8_t)seqs[frames]);
    frames++;return 0;
}
int __wrap_SSL_read(SSL *ssl,void *p,int n)
{
    if (ssl != busy_reader) return __real_SSL_read(ssl,p,n);
    if (!busy_reads) frames_before_read = frames;
    busy_reads++;
    /* A slow receive batch on another slot. Each read returns a carrier pong. */
    vr_frame pong={VR_KEEPALIVE_MAIN,VR_KEEPALIVE_PONG,NULL,NULL,0,0};size_t length;
    assert(n>=12&&!vr_frame_encode(&pong,p,(size_t)n,&length));
    usleep(4000);
    return (int)length;
}

int __wrap_SSL_write(SSL *ssl,const void *p,int n)
{
    if(real_tls)return __real_SSL_write(ssl,p,n);
    (void)ssl;assert(n>0&&(unsigned)n<=VR_TX_WRITE_MAX);calls++;
    if(retry_bytes)assert(p==retry_bytes&&(size_t)n==retry_n&&!memcmp(p,retry_copy,retry_n));
    if(want) {
        if(!retry_bytes){retry_bytes=p;retry_n=(size_t)n;memcpy(retry_copy,p,retry_n);}
        want--;return -1;
    }
    retry_bytes=NULL;retry_n=0;assert(!vr_decoder_feed(&decoder,p,(size_t)n,receive_frame,NULL));return n;
}
int __wrap_SSL_get_error(const SSL *ssl,int result)
{if(real_tls)return __real_SSL_get_error(ssl,result);(void)ssl;assert(result==-1);return (int)ssl_error;}

static int enqueue(vr_link *l,unsigned main,unsigned sub,unsigned seq,size_t n)
{
    uint8_t data[VR_DATA_MAX],wire[VR_FRAME_MAX];assert(n<=sizeof(data));memset(data,(int)seq,n);
    vr_frame f={(uint16_t)main,(uint16_t)sub,NULL,data,0,n};size_t size;
    assert(!vr_frame_encode(&f,wire,sizeof(wire),&size));return vr_link_queue(l,wire,size);
}
static void reset(vr_link *l,SSL_CTX *ctx)
{
    vr_link_close(l,"");assert(!l->used&&!l->tx_memory&&!l->writing);
    for(unsigned lane=0;lane<3;lane++)assert(!l->tx_head[lane]&&!l->tx_tail[lane]&&!l->tx_bytes[lane]);
    l->ssl=SSL_new(ctx);assert(l->ssl);l->client=1;
    memset(&decoder,0,sizeof(decoder));
    frames=calls=want=0;retry_bytes=NULL;retry_n=0;ssl_error=SSL_ERROR_WANT_WRITE;
}
static void poll_one(vr_link *l, short events)
{
    struct pollfd fd = {l->fd, POLLIN | (l->write_events ? l->write_events : POLLOUT), events};
    vr_links_close_failed(l, 1, &fd);
    vr_links_poll(l, 1, &fd);
}

static void drain(vr_link *l)
{
    unsigned loops=0;while(l->used&&loops++<4096)poll_one(l,POLLOUT|POLLIN);
    assert(!l->used&&!l->tx_memory&&!l->writing&&!l->write_pending);
}

static void test_shared_send_budget(SSL_CTX *ctx)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);
    e->link_count=VR_CARRIER_MAX;
    for(size_t i=0;i<VR_CARRIER_MAX;i++) {
        vr_link *l=&e->links[i];l->engine=e;l->fd=-1;reset(l,ctx);
        l->decoder=calloc(1,sizeof(*l->decoder));assert(l->decoder);e->rx_queue_memory+=sizeof(*l->decoder);
    }
    for(size_t i=0;i<VR_CARRIER_MAX;i++)while(!enqueue(&e->links[i],1,1,5,32u*1024u)){}
    size_t total=e->tx_queue_memory+e->rx_queue_memory+e->tx_spare_memory;
    assert(total<=8u*1024u*1024u&&total>7u*1024u*1024u&&e->tx_queue_peak<=8u*1024u*1024u);
    printf("PASS: carrier RX and TX share %zu of %u bytes with %u slots\n",total,VR_TX_TOTAL_CAP,VR_CARRIER_MAX);
    vr_link *idle=&e->links[VR_CARRIER_MAX-1];
    assert(!idle->tx_memory&&!vr_link_keepalive(idle,VR_KEEPALIVE_PING));
    vr_link_close(&e->links[0],"");
    assert(!enqueue(idle,1,1,6,32u*1024u));
    drain(idle);
    for(size_t i=0;i<VR_CARRIER_MAX;i++)vr_link_close(&e->links[i],"");
    assert(!e->tx_queue_memory&&!e->rx_queue_memory&&!e->tx_spare_memory);
    free(e);
}

static void test_transit_pressure_and_reuse(SSL_CTX *ctx)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);strcpy(e->config.node_id,"26");
    e->policy=json_tokener_parse("{\"topology_rules\":["
        "{\"id\":\"vrouter-up\",\"enabled\":true,\"from_node_id\":\"27\",\"to_node_id\":\"26\"},"
        "{\"id\":\"vrouter-down\",\"enabled\":true,\"from_node_id\":\"26\",\"to_node_id\":\"16\"}]}");
    json_object *rules=vr_member(e->policy,"topology_rules");
    for(size_t i=0;i<2;i++)assert(!vr_links_add(e,json_object_array_get_idx(rules,i)));
    vr_link *up=&e->links[0],*down=&e->links[VR_CARRIER_SLOTS];reset(up,ctx);reset(down,ctx);
    const char *path="{\"path\":[\"27\",\"26\",\"16\"]}";
    uint8_t packet[1500]={0x45,0,5,220,0,0,0,0,64,6};
    vr_frame frame={1,1,(const uint8_t *)path,packet,strlen(path),sizeof(packet)};
    /* The fixtures decode TLS output independently here. Unit scope is queue
       admission, allocated bytes and the authenticated transit dispatcher. */
    assert(!vr_link_receive(&frame,up)&&down->used&&!up->tun_pressure_reports);
    vr_link_close(down,"");reset(down,ctx);
    while(!enqueue(down,1,1,5,16u*1024u)){}
    assert(!vr_link_receive(&frame,up)&&up->tun_pressure_reports==1);
    assert(!vr_link_receive(&frame,up)&&up->tun_pressure_reports==1);
    vr_link_close(down,"");reset(down,ctx);
    up->tun_pressure_at=0;
    const char *report="{\"request_id\":\"tun-pressure-v1\",\"tun_pressure_v1\":true}";
    const char *reverse="{\"path\":[\"16\",\"26\"]}";
    vr_frame hint={2,2,(const uint8_t *)reverse,(const uint8_t *)report,strlen(reverse),strlen(report)};
    assert(!vr_link_receive(&hint,down)&&down->peer_pressure_at);
    assert(!vr_link_receive(&frame,up)&&up->tun_pressure_reports==2);
    vr_link_close(down,"");reset(down,ctx);
    for(unsigned i=0;i<8;i++){assert(!enqueue(down,1,1,5,sizeof(packet)));drain(down);}
    assert(e->tx_reused==7&&e->tx_spare_memory<=VR_TX_REUSE_CAP);
    for(size_t i=0;i<e->link_count;i++)vr_link_close(&e->links[i],"");
    assert(!e->tx_queue_memory&&!e->tx_spare_memory);
    json_object_put(e->policy);free(e);
    puts("PASS: authenticated multi-hop pressure and reusable carrier buffers");
}

static void test_tun_pressure_report(SSL_CTX *ctx)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);
    strcpy(e->config.node_id,"26");
    vr_link *l=&e->links[0];l->engine=e;l->fd=-1;strcpy(l->peer,"27");reset(l,ctx);
    vr_link_report_tun_pressure(l,1000);
    assert(l->tx_enqueued==1&&l->tx_head[0]&&!l->tx_head[1]);
    vr_link_report_tun_pressure(l,1050);
    assert(l->tx_enqueued==1);
    vr_link_report_tun_pressure(l,1300);
    assert(l->tx_enqueued==2);
    vr_link_close(l,"");free(e);
}

static void test_carrier_delays_unqueued_ip_frames(SSL_CTX *ctx)
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);strcpy(e->config.node_id,"26");
    e->policy=json_tokener_parse("{\"topology_rules\":["
        "{\"id\":\"vrouter-delay\",\"enabled\":true,\"from_node_id\":\"26\",\"to_node_id\":\"27\"}],\"probe_ips\":[]}");
    assert(e->policy&&!vr_links_add(e,json_object_array_get_idx(vr_member(e->policy,"topology_rules"),0)));
    vr_link *l=&e->links[0];reset(l,ctx);
    unsigned preceding=0;
    while(!enqueue(l,1,1,5,1500))preceding++;
    assert(preceding>0);
    char path[VR_PATH_MAX][129]={{"26"},{"27"}};
    uint8_t data[1500];memset(data,6,sizeof(data));
    assert(!vr_send(e,1,1,path,2,data,sizeof(data)));
    memset(data,7,sizeof(data));
    assert(!vr_send(e,1,1,path,2,data,sizeof(data)));
    usleep(700000);
    assert(vr_carrier_tun_paused(e)&&l->tx_retry_bytes&&l->tx_retry_bytes==e->tx_retry_memory);
    drain(l);
    assert(frames==preceding+2&&seqs[preceding]==6&&seqs[preceding+1]==7);
    assert(!vr_carrier_tun_paused(e)&&!e->tx_retry_memory&&!l->tx_retry_dropped);

    reset(l,ctx);preceding=0;
    while(!enqueue(l,1,1,5,1500))preceding++;
    memset(data,6,sizeof(data));
    assert(!vr_send(e,1,1,path,2,data,sizeof(data)));
    usleep((VR_QUEUE_WAIT_MS+30)*1000);
    drain(l);
    assert(frames==preceding);
    assert(!e->tx_retry_memory&&l->tx_retry_dropped==1);
    vr_link_close(l,"");json_object_put(e->policy);free(e);
    puts("PASS: delayed carrier admission sends exactly once and expires stalled packets");
}

static void test_poll_dispatch(SSL_CTX *ctx)
{
    vr_link *links = calloc(2,sizeof(*links));
    assert(links);
    for (size_t i=0; i<2; i++) {
        links[i].fd = -1;
        reset(&links[i],ctx);
    }
    vr_engine *engine=calloc(1,sizeof(*engine));assert(engine);
    links[0].engine=engine;busy_reader = links[0].ssl;
    /* Neither fd requested writes when poll started. TUN supplies work on
       the second carrier immediately after the first becomes readable. */
    struct pollfd fds[] = {{-1,POLLIN,POLLIN},{-1,POLLIN,0}};
    assert(!enqueue(&links[1],1,1,5,52));
    vr_links_poll(links,2,fds);
    assert(frames_before_read == 1 && busy_reads == 1 && !links[1].used);
    assert(links[0].decoder&&engine->rx_queue_memory==sizeof(vr_decoder));

    /* The first speculative write hits backpressure. Other receive work
       proceeds, and only a subsequent writable event may retry the write. */
    assert(!enqueue(&links[1],1,1,6,52));
    want=1;
    vr_links_poll(links,2,fds);
    unsigned blocked_calls=calls;
    assert(links[1].write_events==POLLOUT && busy_reads==2);
    fds[1].events |= POLLOUT;
    vr_links_poll(links,2,fds);
    assert(calls==blocked_calls && busy_reads==3);
    fds[1].revents=POLLOUT;
    vr_links_poll(links,2,fds);
    assert(frames==2 && seqs[0]==5 && seqs[1]==6 && busy_reads==4);
    busy_reader=NULL;
    for (size_t i=0; i<2; i++) vr_link_close(&links[i],"");
    assert(!engine->rx_queue_memory&&!engine->tx_spare_memory);
    free(engine);free(links);
    puts("PASS: new TUN output sent before busy TLS input in the same poll, backpressure and RX fairness retained");
}

typedef struct {SSL_CTX *ctx;int listener;} tls_peer;

static void test_inbound_cipher(SSL_CTX *server_ctx,const char *offered,int version,const char *expected)
{
    SSL_CTX *client_ctx=SSL_CTX_new(TLS_client_method());
    assert(client_ctx&&SSL_CTX_set_min_proto_version(client_ctx,version)==1);
    assert(SSL_CTX_set_max_proto_version(client_ctx,version)==1);
    if(offered)assert(SSL_CTX_set_ciphersuites(client_ctx,offered)==1);
    SSL *client=SSL_new(client_ctx),*server=SSL_new(server_ctx);
    BIO *client_io=NULL,*server_io=NULL;
    assert(client&&server&&BIO_new_bio_pair(&client_io,0,&server_io,0)==1);
    SSL_set_bio(client,client_io,client_io);
    SSL_set_bio(server,server_io,server_io);
    SSL_set_connect_state(client);
    SSL_set_accept_state(server);
    for(int i=0;i<16&&(!SSL_is_init_finished(client)||!SSL_is_init_finished(server));i++) {
        SSL *peers[]={client,server};
        for(size_t j=0;j<2;j++) {
            SSL *peer=peers[j];
            if(SSL_is_init_finished(peer))continue;
            int result=SSL_do_handshake(peer);
            if(result!=1)assert(SSL_get_error(peer,result)==SSL_ERROR_WANT_READ);
        }
    }
    assert(SSL_is_init_finished(client)&&SSL_is_init_finished(server));
    assert(SSL_version(server)==version);
    if(expected)assert(!strcmp(SSL_get_cipher_name(server),expected));
    SSL_free(server);
    SSL_free(client);
    SSL_CTX_free(client_ctx);
}

static void *slow_peer(void *arg)
{
    tls_peer *peer=arg;int fd=accept(peer->listener,NULL,NULL);assert(fd>=0);
    struct timeval timeout={10,0};assert(!setsockopt(fd,SOL_SOCKET,SO_RCVTIMEO,&timeout,sizeof(timeout)));
    SSL *ssl=SSL_new(peer->ctx);assert(ssl);SSL_set_fd(ssl,fd);assert(SSL_accept(ssl)==1);
    usleep(100000);uint8_t data[4096];
    while(frames<4) {
        int n=SSL_read(ssl,data,sizeof(data));assert(n>0);
        assert(!vr_decoder_feed(&decoder,data,(size_t)n,receive_frame,NULL));usleep(5000);
    }
    SSL_free(ssl);close(fd);return NULL;
}
static void test_real_tls(const char *certfile,const char *keyfile)
{
    char cert[8192],key[8192];size_t n;
    assert(!vr_read_file(certfile,cert,sizeof(cert),&n));cert[n]=0;
    assert(!vr_read_file(keyfile,key,sizeof(key),&n));key[n]=0;
    SSL_CTX *ctx=vr_tls_server_context(cert,key);assert(ctx);
    real_tls=1;
    test_inbound_cipher(ctx,"TLS_AES_128_GCM_SHA256:TLS_CHACHA20_POLY1305_SHA256",TLS1_3_VERSION,"TLS_CHACHA20_POLY1305_SHA256");
    test_inbound_cipher(ctx,"TLS_AES_128_GCM_SHA256",TLS1_3_VERSION,"TLS_AES_128_GCM_SHA256");
    test_inbound_cipher(ctx,NULL,TLS1_2_VERSION,NULL);
    tls_peer peer={ctx,socket(AF_INET,SOCK_STREAM,0)};assert(peer.listener>=0);
    struct sockaddr_in address={0};address.sin_family=AF_INET;address.sin_addr.s_addr=htonl(INADDR_LOOPBACK);
    assert(!bind(peer.listener,(struct sockaddr *)&address,sizeof(address))&&!listen(peer.listener,1));
    socklen_t length=sizeof(address);assert(!getsockname(peer.listener,(struct sockaddr *)&address,&length));
    pthread_t thread;frames=0;memset(&decoder,0,sizeof(decoder));
    assert(!pthread_create(&thread,NULL,slow_peer,&peer));
    /* The TLS listener is local test material. Pin exactly its public key. */
    BIO *bio=BIO_new_mem_buf(cert,-1);X509 *x=PEM_read_bio_X509(bio,NULL,NULL,NULL);assert(x);
    unsigned char *der=NULL,digest[32];int size=i2d_X509_PUBKEY(X509_get_X509_PUBKEY(x),&der);assert(size>0);
    SHA256(der,(size_t)size,digest);OPENSSL_free(der);X509_free(x);BIO_free(bio);
    char pin[65];for(unsigned i=0;i<32;i++)snprintf(pin+2*i,3,"%02x",digest[i]);
    vr_link *l=calloc(1,sizeof(*l));assert(l);l->fd=-1;l->client=1;
    l->ssl=vr_tls_connect_at("127.0.0.1","127.0.0.1",ntohs(address.sin_port),pin,NULL,&l->fd,vr_millis()+5000);assert(l->ssl);
    int small=4096;assert(!setsockopt(l->fd,SOL_SOCKET,SO_SNDBUF,&small,sizeof(small)));
    for(unsigned i=0;i<3;i++)assert(!enqueue(l,1,1,i,60000));
    poll_one(l,POLLOUT);assert(l->used);assert(!enqueue(l,2,1,99,64));
    uint64_t deadline=vr_millis()+10000;
    while(l->used&&vr_millis()<deadline) {
        struct pollfd fd={l->fd,(short)(POLLIN|(l->write_events?l->write_events:POLLOUT)),0};
        assert(poll(&fd,1,20)>=0);poll_one(l,fd.revents);assert(l->ssl);
    }
    assert(!l->used&&!l->tx_memory);pthread_join(thread,NULL);
    assert(frames==4&&mains[0]==1&&mains[3]==1);unsigned business=0,control=0;
    for(unsigned i=0;i<frames;i++){if(mains[i]==1)assert(seqs[i]==business++);else {assert(mains[i]==2);control++;}}
    assert(business==3&&control==1);vr_link_close(l,"");free(l);close(peer.listener);SSL_CTX_free(ctx);
    puts("PASS: real TLS slow consumer, control before backlog, complete 180000-byte ordered business transfer");
}

int main(int argc,char **argv)
{
    if(argc==3){test_real_tls(argv[1],argv[2]);return 0;}assert(argc==1);
    SSL_CTX *ctx=SSL_CTX_new(TLS_method());assert(ctx);
    test_tun_pressure_report(ctx);
    test_carrier_delays_unqueued_ip_frames(ctx);
    test_shared_send_budget(ctx);
    test_transit_pressure_and_reuse(ctx);
    test_poll_dispatch(ctx);
    vr_link *l=calloc(1,sizeof(*l));assert(l);l->fd=-1;reset(l,ctx);
    /* Filling business/bulk must leave capacity for ping and pong. */
    unsigned count=0;while(!enqueue(l,1,1,count%256,2048))count++;
    assert(count>64&&l->tx_memory<VR_TX_CAP);
    assert(!enqueue(l,2,1,201,64)&&!vr_link_keepalive(l,VR_KEEPALIVE_PING));
    assert(enqueue(l,1,1,0,2048)<0);drain(l);
    assert(frames==count+1&&mains[0]==2);
    assert(l->tx_wait_samples==count+2&&!l->tx_backlog_since);
    assert(vr_link_oldest_wait_ms(l,vr_millis())==0);
    for(unsigned i=1;i<frames;i++)assert(mains[i]==1&&seqs[i]==(i-1)%256);

    /* Weighted fairness and per-class FIFO (including speed finish). */
    reset(l,ctx);
    for(unsigned i=0;i<24;i++)assert(!enqueue(l,1,1,i,256));
    for(unsigned i=0;i<4;i++)assert(!enqueue(l,4,i==3?3:2,i,256));
    drain(l);unsigned business=0,bulk=0;
    for(unsigned i=0;i<frames;i++) {
        if(mains[i]==4){assert(i==8||i==17||i==26||i==27);assert(seqs[i]==bulk++);}
        else assert(seqs[i]==business++);
    }
    assert(business==24&&bulk==4);

    /* Ready packet frames share a TLS write without delaying a lone frame.
       A retry retains its bytes even if a priority frame arrives. */
    reset(l,ctx);
    for(unsigned i=0;i<8;i++)assert(!enqueue(l,1,1,i,52));
    want=1;poll_one(l,POLLOUT);
    assert(calls==1&&!frames);
    assert(!enqueue(l,2,1,9,64));drain(l);
    assert(calls==3&&frames==9&&mains[8]==2);
    for(unsigned i=0;i<8;i++)assert(mains[i]==1&&seqs[i]==i);

    /* WANT_WRITE: same pointer, length and bytes on every retry, even after
       a high priority frame arrives. Complete the current frame first. */
    reset(l,ctx);assert(!enqueue(l,8,3,1,60000));assert(!enqueue(l,8,4,2,64));
    want=2;poll_one(l,POLLOUT);assert(l->writing&&l->write_pending);
    assert(!enqueue(l,3,1,3,64));poll_one(l,POLLOUT);assert(!frames);
    drain(l);assert(frames==3&&mains[0]==8&&subs[0]==3&&mains[1]==3&&subs[2]==4);

    /* WANT_READ must not spin on a writable socket or change retry data. */
    reset(l,ctx);assert(!enqueue(l,1,1,1,2048));want=1;ssl_error=SSL_ERROR_WANT_READ;
    poll_one(l,POLLOUT);assert(l->write_events==POLLIN);unsigned before=calls;
    poll_one(l,POLLOUT);assert(calls==before);
    poll_one(l,POLLIN);assert(!l->used&&frames==1);

    /* Tiny frames cannot allocate unbounded list metadata. Bulk also has its
       own cap so a speed test cannot consume the entire business budget. */
    reset(l,ctx);count=0;while(!enqueue(l,4,2,0,8192))count++;
    assert(count>=4&&count<=12&&l->tx_memory_class[2]<=VR_TX_BULK_CAP);
    assert(!enqueue(l,8,1,0,2048)&&!enqueue(l,2,1,0,64));
    reset(l,ctx);while(!vr_link_keepalive(l,VR_KEEPALIVE_PING))assert(l->tx_memory<=VR_TX_CAP);
    assert(l->tx_memory_class[0]<=VR_TX_CONTROL_CAP);assert(!enqueue(l,1,1,0,2048));
    /* Every lane must accept a maximum legal wire frame, not silently lower
       the existing protocol's data/control limits. */
    for(unsigned type=2;type<=4;type+=2) {
        reset(l,ctx);uint8_t control[VR_CONTROL_MAX],data[VR_DATA_MAX],wire[VR_FRAME_MAX];
        memset(control,' ',sizeof(control));memset(data,77,sizeof(data));
        vr_frame frame={(uint16_t)type,1,control,data,sizeof(control),sizeof(data)};size_t size;
        assert(!vr_frame_encode(&frame,wire,sizeof(wire),&size));
        assert(!vr_link_queue(l,wire,size));drain(l);assert(frames==1&&mains[0]==type);
    }
    reset(l,ctx);assert(vr_link_queue(l,"",SIZE_MAX)<0);

    /* Cleanup includes a frame held by a failed SSL_write. */
    assert(!enqueue(l,1,1,0,2048));want=1;poll_one(l,POLLOUT);assert(l->writing);
    reset(l,ctx);vr_link_close(l,"");SSL_CTX_free(ctx);free(l);
    puts("PASS: bounded priority queues, control reserve, fair bulk, intact main=8/speed order and immutable TLS WANT retries");return 0;
}
