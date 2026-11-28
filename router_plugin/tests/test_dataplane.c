#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <assert.h>
#include <fcntl.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

static uint8_t carried[1500];static size_t carried_len;
static uint64_t drain_clock;
static unsigned drain_forwarded, packet_cost_ms;
static int writer_fixture_fd=-1;
static pthread_t writer_test_thread;
static pthread_mutex_t writer_gate=PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t writer_release=PTHREAD_COND_INITIALIZER;
static int writer_released;
static unsigned writer_packets;
ssize_t __real_write(int fd,const void *bytes,size_t n);
ssize_t __wrap_write(int fd,const void *bytes,size_t n)
{
    if(fd!=writer_fixture_fd)return __real_write(fd,bytes,n);
    assert(!pthread_equal(pthread_self(),writer_test_thread));
    assert(n==sizeof(unsigned));
    unsigned number;
    memcpy(&number,bytes,n);
    assert(number==writer_packets++);
    if(number==0) {
        pthread_mutex_lock(&writer_gate);
        while(!writer_released)pthread_cond_wait(&writer_release,&writer_gate);
        pthread_mutex_unlock(&writer_gate);
    }
    return (ssize_t)n;
}
uint64_t __real_vr_millis(void);
uint64_t __wrap_vr_millis(void)
{
    return drain_clock ? drain_clock : __real_vr_millis();
}
static int resolved_requests,controller_requests,external_fixture;
int __wrap_vr_resolve4(const char *domain,const char *upstream,const char *iface,char out[16])
{(void)upstream;(void)iface;assert(!strcmp(domain,"local.example"));resolved_requests++;strcpy(out,"203.0.113.5");return 0;}
static void tcp_checksums(uint8_t b[40])
{
    b[10]=b[11]=b[36]=b[37]=0;uint16_t c=vr_checksum(b,20);b[10]=(uint8_t)(c>>8);b[11]=(uint8_t)c;
    uint8_t pseudo[32];memcpy(pseudo,b+12,8);pseudo[8]=0;pseudo[9]=6;pseudo[10]=0;pseudo[11]=20;memcpy(pseudo+12,b+20,20);
    c=vr_checksum(pseudo,sizeof(pseudo));b[36]=(uint8_t)(c>>8);b[37]=(uint8_t)c;
}
json_object *__wrap_vr_controller_call(const vr_config *c,const char *iface,const char *method,const char *path,json_object *body,char *error,size_t capacity)
{
    (void)c;(void)iface;(void)method;(void)body;(void)error;(void)capacity;
    assert(!strcmp(path,"/api/probe/route/fake_ip/resolve"));controller_requests++;usleep(200000);
    const char *reply="{\"item\":{\"domain\":\"203.0.113.5\",\"fake_ip\":\"198.18.0.20\",\"action\":\"probe_exit\",\"exit_node_id\":\"8\",\"expires_at\":\"2099-01-01T00:00:00Z\"}}";
    return vr_json_parse(reply,strlen(reply));
}
int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n)
{memset(path,0,VR_PATH_MAX*129);strcpy(path[0],e->config.node_id);strcpy(path[1],target);*n=2;return 0;}
int vr_packet_path(vr_engine *e,const char *target,const uint8_t *packet,size_t length,char path[VR_PATH_MAX][129],size_t *n)
{(void)packet;(void)length;return vr_path(e,target,path,n);}

int vr_send(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,const void *b,size_t len)
{
    (void)e;(void)path;assert(main==1&&sub==1&&n==2&&len<=sizeof(carried));memcpy(carried,b,len);carried_len=len;
    if (drain_clock) {
        assert(((unsigned)carried[4]<<8 | carried[5]) == drain_forwarded++);
        drain_clock += packet_cost_ms;
    }
    return 0;
}

static vr_engine *engine(const char *id,const char *ip,int pipes[2])
{
    vr_engine *e=calloc(1,sizeof(*e));assert(e);pthread_mutex_init(&e->mutex,NULL);vr_config_default(&e->config);
    strcpy(e->config.node_id,id);strcpy(e->own_ip,ip);strcpy(e->cidr,"198.18.0.0/15");
    const char *policy="{\"probe_ips\":[{\"node_id\":\"7\",\"ip\":\"198.18.0.7\"},{\"node_id\":\"8\",\"ip\":\"198.18.0.8\"}]}";
    e->policy=vr_json_parse(policy,strlen(policy));assert(e->policy);assert(!pipe2(pipes,O_NONBLOCK|O_CLOEXEC));e->tun=pipes[1];
    vr_mapping *m=&e->mappings[0];strcpy(m->domain,"203.0.113.5");strcpy(m->ip,"198.18.0.20");strcpy(m->exit,"8");m->expires=time(NULL)+60;
    return e;
}

static void test_fake_dns_blacklist(void)
{
    int pipes[2];vr_engine *e=engine("7","198.18.0.7",pipes);
    strcpy(e->mappings[0].domain,"dns.google.com");
    const uint8_t udp[]={0x45,0,0,28,0,0,0x40,0,64,17,0,0,0,0,0,0,0,0,0,0,0xbe,0xef,1,0xbb,0,8,0,0};
    uint8_t tcp[40]={0x45,0,0,40,0,0,0x40,0,64,6};tcp[32]=0x50;tcp[33]=2;tcp[27]=10;tcp_checksums(tcp);
    int before_http=controller_requests,before_dns=resolved_requests;
    for(int protocol=0;protocol<2;protocol++) {
        uint8_t request[40];size_t len=protocol?sizeof(tcp):sizeof(udp);memcpy(request,protocol?tcp:udp,len);
        if(!protocol){uint16_t sum=vr_checksum(request,20);request[10]=(uint8_t)(sum>>8);request[11]=(uint8_t)sum;}
        assert(!vr_packet_rewrite(request,len,inet_addr("192.168.52.100"),inet_addr("198.18.0.20"),49000,443));
        uint8_t wire[40];memcpy(wire,request,len);carried_len=0;vr_outgoing(e,wire,len);assert(carried_len==len);
        if(protocol){request[33]=0x10;tcp_checksums(request);} /* existing TCP flow */
        e->config.dns_filter.block_doh=1;e->config.dns_filter.count=1;strcpy(e->config.dns_filter.entries[0],"google.com");vr_mapping_dns_refresh(e);
        assert(e->mappings[0].dns_blocked);
        for(int i=0;i<64;i++){memcpy(wire,request,len);carried_len=0;vr_outgoing(e,wire,len);assert(!carried_len&&read(pipes[0],carried,sizeof(carried))<0);}
        /* Removing a list item and disabling the switch both unblock locally. */
        e->config.dns_filter.count=0;vr_mapping_dns_refresh(e);assert(!e->mappings[0].dns_blocked);
        memcpy(wire,request,len);carried_len=0;vr_outgoing(e,wire,len);assert(carried_len==len);
        e->config.dns_filter.block_doh=0;vr_mapping_dns_refresh(e);
    }
    assert(controller_requests==before_http&&resolved_requests==before_dns);
    close(pipes[0]);close(pipes[1]);json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);
    puts("PASS: cached Fake IP blacklist blocks TCP/UDP before established reuse, removes locally, no controller or DNS lookups");
}

static void test_fake_dns_blacklist_scan(void)
{
    int pipes[2];vr_engine *e=engine("7","198.18.0.7",pipes);
    vr_mapping blocked=e->mappings[0];
    strcpy(blocked.domain,"dns.google.com");
    strcpy(blocked.ip,"198.18.0.21");
    e->mappings[VR_MAPPING_MAX-2]=blocked;
    strcpy(blocked.ip,"198.18.0.22");
    e->mappings[VR_MAPPING_MAX-1]=blocked;
    e->config.dns_filter.block_doh=1;
    e->config.dns_filter.count=1;
    strcpy(e->config.dns_filter.entries[0],"google.com");
    vr_mapping_dns_refresh(e);
    e->observation.active=1;
    uint8_t tcp[40]={0x45,0,0,40,0,0,0x40,0,64,6};
    tcp[32]=0x50;tcp[33]=2;tcp_checksums(tcp);
    assert(!vr_packet_rewrite(tcp,sizeof(tcp),inet_addr("192.168.52.100"),inet_addr("198.18.0.20"),49001,443));
    carried_len=0;vr_outgoing(e,tcp,sizeof(tcp));
    assert(carried_len==sizeof(tcp));
    assert(e->observation.stages[VR_OBS_DNS_BLACKLIST].samples==1);
    assert(e->observation.stages[VR_OBS_DNS_BLACKLIST].scan_steps==2);

    for(size_t i=1;i<VR_DNS_BLOCKED_CACHE_MAX;i++) {
        snprintf(blocked.ip,sizeof(blocked.ip),"198.18.1.%u",(unsigned)i);
        e->mappings[i]=blocked;
    }
    vr_mapping_dns_refresh(e);
    size_t scanned;
    assert(e->dns_blocked_count==129);
    assert(vr_mapping_dns_blocked(e,inet_addr("198.18.0.22"),&scanned)&&scanned==129);
    e->config.dns_filter.count=0;
    vr_mapping_dns_refresh(e);
    assert(!vr_mapping_dns_blocked(e,inet_addr("198.18.0.22"),&scanned)&&scanned==0);

    close(pipes[0]);close(pipes[1]);json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);
    puts("PASS: packet checks only populated blacklist entries and keeps overflow blocked");
}

static void test_tun_drain(void)
{
    int pipe_fds[2], pair[2];
    vr_engine *e = engine("7", "198.18.0.7", pipe_fds);
    assert(!socketpair(AF_UNIX, SOCK_DGRAM | SOCK_NONBLOCK, 0, pair));
    e->tun = pair[0];
    uint8_t ack[40] = {0x45,0,0,40,0,0,0x40,0,64,6};
    ack[32] = 0x50;
    ack[33] = 0x10;
    tcp_checksums(ack);
    assert(!vr_packet_rewrite(ack,sizeof(ack),inet_addr("198.18.0.7"),inet_addr("198.18.0.8"),40001,443));
    for (unsigned i = 0; i < 20; i++) {
        ack[5] = (uint8_t)i;
        tcp_checksums(ack);
        assert(write(pair[1], ack, sizeof(ack)) == sizeof(ack));
    }
    drain_clock = 100;
    vr_tun_drain(e);
    assert(drain_forwarded == 16);
    packet_cost_ms = 2;
    vr_tun_drain(e);
    assert(drain_forwarded == 18);
    packet_cost_ms = 0;
    vr_tun_drain(e);
    vr_tun_drain(e);
    assert(drain_forwarded == 20);
    drain_clock = 0;
    close(pair[0]);close(pair[1]);close(pipe_fds[0]);close(pipe_fds[1]);
    json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);
}

static void test_tun_writer(void)
{
    int fds[2];
    vr_engine *e=engine("7","198.18.0.7",fds);
    e->observation.active=1;
    e->observation.generation=1;
    writer_test_thread=pthread_self();
    writer_fixture_fd=e->tun;
    assert(!vr_tun_writer_start(e));
    json_object *stats=vr_tun_writer_status(e);
    unsigned capacity=(unsigned)json_object_get_int(vr_member(stats,"capacity_packets"));
    assert(capacity==64);
    json_object_put(stats);
    for(unsigned i=0;i<256;i++) {
        if(i==capacity) {
            stats=vr_tun_writer_status(e);
            assert(json_object_get_int(vr_member(stats,"queued_packets"))==(int)capacity);
            json_object_put(stats);
            pthread_mutex_lock(&writer_gate);
            writer_released=1;
            pthread_cond_signal(&writer_release);
            pthread_mutex_unlock(&writer_gate);
        }
        vr_tun_write(e,&i,sizeof(i));
    }
    vr_tun_writer_flush(e);
    stats=vr_tun_writer_status(e);
    assert(writer_packets==256);
    assert(json_object_get_int64(vr_member(stats,"written_packets"))==256);
    assert(json_object_get_int(vr_member(stats,"peak_packets"))==(int)capacity);
    assert(json_object_get_int(vr_member(stats,"queued_packets"))==0);
    assert(json_object_get_int(vr_member(stats,"write_errors"))==0);
    assert(e->observation.stages[VR_OBS_TUN_WRITE].calls==256);
    assert(e->observation.stages[VR_OBS_TUN_WRITE].samples==4);
    json_object_put(stats);
    vr_tun_writer_stop(e);
    writer_fixture_fd=-1;
    close(fds[0]);close(fds[1]);json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);
    puts("PASS: TUN worker writes in order, drains bounded backlog, and preserves timing samples");
}

static void wait_reader_queue(vr_engine *e,int depth)
{
    for(unsigned i=0;i<1000;i++) {
        json_object *status=vr_tun_reader_status(e);
        int queued=json_object_get_int(vr_member(status,"queued_packets"));
        json_object_put(status);
        if(queued==depth)return;
        usleep(1000);
    }
    assert(!"TUN reader did not fill its queue");
}

static void test_tun_reader(void)
{
    int pipes[2],pair[2];vr_engine *e=engine("7","198.18.0.7",pipes);
    assert(!socketpair(AF_UNIX,SOCK_DGRAM|SOCK_NONBLOCK,0,pair));
    int send_buffer=512*1024;
    assert(!setsockopt(pair[1],SOL_SOCKET,SO_SNDBUF,&send_buffer,sizeof(send_buffer)));
    e->tun=pair[0];
    assert(!vr_tun_reader_start(e));
    struct pollfd wake={vr_tun_reader_poll_fd(e),POLLIN,0};
    assert(wake.fd!=e->tun&&poll(&wake,1,0)==0);

    uint8_t ack[40]={0x45,0,0,40,0,0,0x40,0,64,6};
    ack[32]=0x50;ack[33]=0x10;tcp_checksums(ack);
    assert(!vr_packet_rewrite(ack,sizeof(ack),inet_addr("198.18.0.7"),inet_addr("198.18.0.8"),40001,443));
    enum { packets=320 };
    for(unsigned i=0;i<packets;i++) {
        ack[4]=(uint8_t)(i>>8);ack[5]=(uint8_t)i;tcp_checksums(ack);
        assert(write(pair[1],ack,sizeof(ack))==sizeof(ack));
    }
    assert(poll(&wake,1,1000)==1&&(wake.revents&POLLIN));
    wait_reader_queue(e,256);
    drain_clock=100;drain_forwarded=0;
    vr_tun_drain(e);
    assert(drain_forwarded==64&&poll(&wake,1,0)==1);
    while(drain_forwarded<packets) {
        assert(poll(&wake,1,1000)==1&&(wake.revents&POLLIN));
        vr_tun_drain(e);
    }
    assert(drain_forwarded==packets&&poll(&wake,1,0)==0);
    drain_clock=0;
    json_object *status=vr_tun_reader_status(e);
    assert(json_object_get_int(vr_member(status,"capacity_packets"))==256);
    assert(json_object_get_int(vr_member(status,"peak_packets"))==256);
    assert(json_object_get_int64(vr_member(status,"read_packets"))==packets);
    assert(json_object_get_int64(vr_member(status,"full_events"))>0);
    assert(json_object_get_int(vr_member(status,"read_errors"))==0);
    json_object_put(status);
    vr_tun_reader_stop(e);
    assert(vr_tun_reader_poll_fd(e)==e->tun);

    assert(!vr_tun_reader_start(e));
    for(unsigned i=0;i<packets;i++)assert(write(pair[1],ack,sizeof(ack))==sizeof(ack));
    wait_reader_queue(e,256);
    vr_tun_reader_stop(e);
    close(pair[0]);close(pair[1]);close(pipes[0]);close(pipes[1]);
    json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);
    puts("PASS: TUN reader keeps order, wakes the engine and shuts down with bounded backpressure");
}

int main(void)
{
    test_fake_dns_blacklist();
    test_fake_dns_blacklist_scan();
    test_tun_drain();
    test_tun_writer();
    test_tun_reader();
    {
        int pipes[2];vr_engine *e=engine("7","198.18.0.7",pipes);
        uint8_t packet[40]={0x45,0,0,40,0,0,0x40,0,64,6};packet[32]=0x50;packet[33]=2;tcp_checksums(packet);
        assert(!vr_packet_rewrite(packet,sizeof(packet),inet_addr("198.18.0.7"),inet_addr("198.19.255.254"),40001,443));
        external_fixture=1;carried_len=0;vr_outgoing(e,packet,sizeof(packet));assert(carried_len==sizeof(packet));
        vr_packet wire;assert(!vr_packet_parse(carried,carried_len,&wire));assert(wire.dst==inet_addr("1.1.1.1"));
        assert(!strcmp(e->flows[0].target,"8"));
        assert(!vr_packet_rewrite(carried,carried_len,wire.dst,wire.src,wire.dport,wire.sport));
        carried[33]=0x12;tcp_checksums(carried);
        char path[VR_PATH_MAX][129]={{"8"},{"7"}};vr_frame reply={1,1,NULL,carried,0,carried_len};vr_incoming(e,&reply,path,2);
        assert(read(pipes[0],packet,sizeof(packet))==sizeof(packet));vr_packet restored;assert(!vr_packet_parse(packet,sizeof(packet),&restored));
        assert(restored.src==inet_addr("198.19.255.254")&&restored.dst==inet_addr("198.18.0.7")&&restored.dport==40001);
        external_fixture=0;close(pipes[0]);close(pipes[1]);json_object_put(e->policy);pthread_mutex_destroy(&e->mutex);free(e);
        puts("PASS: selected external exit rewrites destination and restores exact TCP reply");
    }

    int entry_pipe[2],exit_pipe[2];vr_engine *entry=engine("7","198.18.0.7",entry_pipe),*exit=engine("8","198.18.0.8",exit_pipe);
    const char *hex="450000210001400040117f98c0a83464c6120014ad9c0035000d52fd68656c6c6f";
    uint8_t packet[1500];size_t len=strlen(hex)/2;for(size_t i=0;i<len;i++){unsigned b;assert(sscanf(hex+2*i,"%2x",&b)==1);packet[i]=(uint8_t)b;}
    vr_packet original;assert(!vr_packet_parse(packet,len,&original));
    /* A browser can retain its Fake IP after the DNS blacklist is enabled. */
    strcpy(entry->mappings[0].domain,"dns.google.com");
    entry->config.dns_filter.block_doh=1;entry->config.dns_filter.count=1;
    strcpy(entry->config.dns_filter.entries[0],"dns.google.com");
    vr_mapping_dns_refresh(entry);
    carried_len=0;vr_outgoing(entry,packet,len);assert(!carried_len&&read(entry_pipe[0],carried,sizeof(carried))<0);
    memset(&entry->config.dns_filter,0,sizeof(entry->config.dns_filter));strcpy(entry->mappings[0].domain,"203.0.113.5");vr_mapping_dns_refresh(entry);
    /* Source selects 8 locally while the controller's shared mapping says 9. */
    strcpy(entry->mappings[0].rule,"fake-rule");strcpy(entry->mappings[0].exit,"9");
    entry->mappings[0].local_exit=1;
    const char *rules="[{\"id\":\"fake-rule\",\"action\":\"direct\",\"entries\":[\"domain_suffix:example.com\"]}]";
    json_object_object_add(entry->policy,"route_rules",vr_json_parse(rules,strlen(rules)));
    entry->config.exit_overrides.count=1;strcpy(entry->config.exit_overrides.items[0].rule,"fake-rule");strcpy(entry->config.exit_overrides.items[0].node,"8");
    vr_outgoing(entry,packet,len);assert(carried_len==len);
    char path[VR_PATH_MAX][129]={{"7"},{"8"}};vr_frame frame={1,1,NULL,carried,0,carried_len};
    strcpy(exit->mappings[0].exit,"9"); /* The shared default differs from this chosen exit. */
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
    assert(!strcmp(entry->mappings[0].exit,"9")); /* Local selection never changes shared cache defaults. */
    for(size_t i=0;i<VR_NAT_MAX;i++)entry->flows[i].used=0;
    vr_incoming(entry,&frame,path,2);assert(read(entry_pipe[0],packet,sizeof(packet))<0);
    /* A cached Fake IP observes local actions without changing shared mapping. */
    for(size_t i=0;i<len;i++){unsigned value;assert(sscanf(hex+2*i,"%2x",&value)==1);packet[i]=(uint8_t)value;}
    strcpy(entry->config.exit_overrides.items[0].node,"@reject");carried_len=0;
    vr_outgoing(entry,packet,len);assert(!carried_len&&read(entry_pipe[0],carried,sizeof(carried))<0);
    strcpy(entry->config.exit_overrides.items[0].node,"@direct");
    vr_outgoing(entry,packet,len);assert(!carried_len&&read(entry_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    vr_packet direct;assert(!vr_packet_parse(packet,len,&direct)&&direct.dst==inet_addr("203.0.113.5"));
    assert(!vr_packet_rewrite(packet,len,direct.dst,direct.src,direct.dport,direct.sport));
    vr_outgoing(entry,packet,len);assert(read(entry_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    assert(!vr_packet_parse(packet,len,&reply)&&reply.src==original.dst&&reply.dst==original.src);
    assert(!strcmp(entry->mappings[0].exit,"9")&&!strcmp(entry->mappings[0].rule,"fake-rule"));
    /* Resetting a formerly direct rule must not route a cached Fake IP to its anchor. */
    for(size_t i=0;i<VR_NAT_MAX;i++)entry->flows[i].used=0;
    entry->config.exit_overrides.count=0;
    for(size_t i=0;i<len;i++){unsigned value;assert(sscanf(hex+2*i,"%2x",&value)==1);packet[i]=(uint8_t)value;}
    carried_len=0;vr_outgoing(entry,packet,len);assert(!carried_len&&read(entry_pipe[0],carried,sizeof(carried))==(ssize_t)len);
    json_object *rule=json_object_array_get_idx(vr_member(entry->policy,"route_rules"),0);
    json_object_object_add(rule,"action",json_object_new_string("reject"));
    for(size_t i=0;i<VR_NAT_MAX;i++)entry->flows[i].used=0;
    for(size_t i=0;i<len;i++){unsigned value;assert(sscanf(hex+2*i,"%2x",&value)==1);packet[i]=(uint8_t)value;}
    vr_outgoing(entry,packet,len);assert(!carried_len&&read(entry_pipe[0],carried,sizeof(carried))<0);
    entry->config.exit_overrides.count=1;strcpy(entry->config.exit_overrides.items[0].node,"8");
    vr_outgoing(entry,packet,len);assert(carried_len==len);
    for(size_t i=0;i<VR_NAT_MAX;i++)entry->flows[i].used=0;
    entry->mappings[0].local_exit=0;json_object_object_del(entry->policy,"route_rules");
    strcpy(entry->config.exit_overrides.items[0].node,"8");
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
    json_object_object_add(exit->policy,"route_rules",json_object_new_array());
    assert(!vr_packet_rewrite(packet,len,inet_addr("192.168.52.100"),inet_addr("203.0.113.5"),46000,53));
    carried_len=0;vr_outgoing(entry,packet,len);assert(carried_len==len);
    frame.data=carried;frame.data_len=len;strcpy(path[0],"7");strcpy(path[1],"8");
    vr_incoming(exit,&frame,path,2);assert(read(exit_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    assert(!vr_packet_parse(packet,len,&request)&&request.dst==inet_addr("203.0.113.5"));
    assert(!vr_packet_rewrite(packet,len,request.dst,request.src,request.dport,request.sport));
    vr_outgoing(exit,packet,len);strcpy(path[0],"8");strcpy(path[1],"7");vr_incoming(entry,&frame,path,2);
    assert(read(entry_pipe[0],packet,sizeof(packet))==(ssize_t)len);
    /* An exit assigned to self reinjects once for physical WAN NAT. */
    json_object_object_add(exit->policy,"route_rules",vr_json_parse(cidr_rule,strlen(cidr_rule)));
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
    /* A cold local direct destination resolves off-loop even if shared exit is 9. */
    int async_pipe[2];vr_engine *local=engine("7","198.18.0.7",async_pipe);
    strcpy(local->mappings[0].domain,"local.example");strcpy(local->mappings[0].rule,"fake-rule");strcpy(local->mappings[0].exit,"9");
    local->config.exit_overrides.count=1;strcpy(local->config.exit_overrides.items[0].rule,"fake-rule");strcpy(local->config.exit_overrides.items[0].node,"@direct");
    for(size_t i=0;i<len;i++){unsigned value;assert(sscanf(hex+2*i,"%2x",&value)==1);packet[i]=(uint8_t)value;}
    assert(!vr_dataplane_start(local));carried_len=0;vr_outgoing(local,packet,len);ssize_t got=-1;
    for(unsigned i=0;i<1000&&got<0;i++){vr_dataplane_tick(local);got=read(async_pipe[0],packet,sizeof(packet));if(got<0)usleep(1000);}
    assert(got==(ssize_t)len&&!carried_len&&resolved_requests==1);
    assert(!vr_packet_parse(packet,len,&direct)&&direct.dst==inet_addr("203.0.113.5"));
    assert(!strcmp(local->mappings[0].exit,"9"));vr_dataplane_stop(local);
    close(async_pipe[0]);close(async_pipe[1]);json_object_put(local->policy);pthread_mutex_destroy(&local->mutex);free(local);
    close(entry_pipe[0]);close(entry_pipe[1]);close(exit_pipe[0]);close(exit_pipe[1]);
    json_object_put(entry->policy);json_object_put(exit->policy);pthread_mutex_destroy(&entry->mutex);pthread_mutex_destroy(&exit->mutex);free(entry);free(exit);
    puts("PASS: LAN fake-IP NAT round trip, spoof rejection, bounded asynchronous mapping resolution");return 0;
}

int vr_external_target(vr_engine *e,const vr_packet *p,uint32_t *real,char node[129]){(void)e;if(!external_fixture||p->src!=inet_addr("198.18.0.7")||p->dst!=inet_addr("198.19.255.254")||p->sport!=40001||p->dport!=443||p->protocol!=6)return 0;*real=inet_addr("1.1.1.1");strcpy(node,"8");return 1;}
