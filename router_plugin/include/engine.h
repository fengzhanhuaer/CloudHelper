#ifndef CHVR_ENGINE_H
#define CHVR_ENGINE_H
#include "service.h"
#include "transport.h"
#include "platform.h"
#include "packet.h"
#include <netinet/in.h>

#define VR_TX_CAP (256u*1024u)
#define VR_NAT_MAX 2048
#define VR_SPEED_BYTES (128u*1024u*1024u)
#define VR_SPEED_DURATION_MS 10000u
#define VR_SPEED_TIMEOUT_MS (VR_SPEED_DURATION_MS+20000u)
typedef struct vr_engine vr_engine;
typedef struct {
    vr_engine *engine;
    json_object *rule;
    char route[33],peer[129],error[96],ping_id[49];
    SSL *ssl;
    int fd,client;
    uint64_t retry,rx,tx,last_rx,ping_time,rtt;
    time_t auth_expires;
    vr_decoder decoder;
    vr_ws_decoder ws;
    uint8_t queue[VR_TX_CAP];size_t head,used,write_pending;
} vr_link;
typedef struct {vr_engine *engine;pthread_t thread;int started,done,fd,index;vr_link *result;} vr_handshake;
typedef struct {char domain[254],ip[16],rule[129],exit[129];time_t expires;uint64_t used;uint32_t resolved;} vr_mapping;
#define VR_MAPPING_TTL (48*60*60)
/* Atomic replacement can temporarily retain both the old and new file. */
#define VR_MAPPING_FILE_MAX ((1024u*1024u-1u)/2u)
typedef struct {char key[65];int busy;uint64_t retry,used;} vr_mapping_pending;
typedef struct {int state,incoming;char ip[16],path[VR_PATH_MAX][129];size_t n,count;uint8_t packet[1500];} vr_resolve_job;
typedef struct {vr_packet original,wire;int entry;uint64_t used;} vr_nat;
typedef struct {int tcp;size_t n;uint8_t query[4096];struct sockaddr_in source;socklen_t source_len;} vr_dns_job;
typedef struct {char id[129],path[VR_PATH_MAX][129];size_t count;uint64_t first,last,bytes,frames,limit,deadline;json_object *message;} vr_speed_rx;
struct vr_engine {
    vr_service *service;vr_config config;unsigned generation;
    vr_addresses addresses;
    json_object *snapshot,*policy,*nonces;
    char own_ip[16],cidr[64],replay_path[512],error[256];
    SSL_CTX *tls;
    vr_link links[VR_LINK_MAX];size_t link_count;
    vr_handshake handshakes[2];
    pthread_t control_thread;int control_started,control_stop,controller_connected,refresh;
    int tun,lan,wan,dns_udp,dns_tcp,platform_started;
    pthread_mutex_t mutex;pthread_cond_t jobs_changed;
    vr_mapping mappings[VR_MAPPING_MAX];
    vr_mapping_pending mapping_pending[16];
    char mapping_path[512],mapping_scope[65];
    unsigned mapping_dirty;uint64_t mapping_flush_at;
    vr_nat flows[VR_NAT_MAX];uint16_t next_port;
    vr_resolve_job resolves[16];pthread_t resolve_thread;
    pthread_cond_t resolve_changed;int resolve_started,resolve_stop;
    vr_dns_job jobs[16];size_t job_head,job_count;
    int dns_stop;pthread_t dns_workers[4];size_t dns_worker_count;
    vr_speed_rx receives[4];
    json_object *speed_send;
    char speed_path[VR_PATH_MAX][129],measure_id[49];size_t speed_count;
    uint64_t speed_started,speed_bytes,speed_limit,speed_duration,measure_started,measure_timeout;
};

int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n);
int vr_send(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,const void *data,size_t len);
int vr_send_json(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,json_object *message);
void vr_link_close(vr_link *l,const char *error);
int vr_link_queue(vr_link *l,int opcode,const void *bytes,size_t n);
void vr_link_dial(vr_link *l);
void vr_link_accept(vr_engine *e,int fd);
void vr_link_poll(vr_link *l,short events);
int vr_link_receive(const vr_frame *frame,void *link);
void vr_handshakes_poll(vr_engine *e,int finish);
int vr_control_start(vr_engine *e);
void vr_control_stop(vr_engine *e);
void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count);
int vr_mapping_get(vr_engine *e,const char *domain,const char *ip,vr_decision decision,vr_mapping *out);
int vr_mapping_lookup(vr_engine *e,const char *domain,const char *ip,vr_decision decision,vr_mapping *out);
void vr_mapping_load(vr_engine *e);
int vr_mapping_flush(vr_engine *e,int force);
void vr_outgoing(vr_engine *e,uint8_t *bytes,size_t n);
int vr_dataplane_start(vr_engine *e);
void vr_dataplane_tick(vr_engine *e);
void vr_dataplane_stop(vr_engine *e);
void vr_incoming(vr_engine *e,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count);
int vr_dns_start(vr_engine *e);
void vr_dns_accept(vr_engine *e,int tcp);
void vr_dns_stop(vr_engine *e);
void vr_measure_frame(vr_engine *e,vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n);
void vr_measure_tick(vr_engine *e);
void vr_measure_stop(vr_engine *e);
#endif
