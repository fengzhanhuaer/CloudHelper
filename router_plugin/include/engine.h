#ifndef CHVR_ENGINE_H
#define CHVR_ENGINE_H
#include "service.h"
#include "transport.h"
#include "tcp_window.h"
#include "platform.h"
#include "packet.h"
#include "observe.h"
#include "network_monitor.h"
#include <netinet/in.h>
#include <poll.h>

#define VR_TX_CAP (256u*1024u)
#define VR_TX_CONTROL_RESERVE (32u*1024u)
#define VR_TX_CONTROL_CAP (96u*1024u)
#define VR_TX_BULK_CAP (96u*1024u)
#define VR_TX_WRITE_MAX (16u*1024u)
#define VR_NAT_MAX 2048
#define VR_TCP_MAX 256
#define VR_TCP_POLL_MAX (1+VR_TCP_MAX*2)
#define VR_SPEED_BYTES (128u*1024u*1024u)
#define VR_SPEED_DURATION_MS 10000u
#define VR_SPEED_TIMEOUT_MS (VR_SPEED_DURATION_MS+20000u)
typedef struct vr_engine vr_engine;
typedef struct vr_tx_frame vr_tx_frame;
typedef struct {
    vr_engine *engine;
    json_object *rule;
    char route[33],peer[129],error[96],ping_id[49];
    SSL *ssl;
    vr_tcp_windows windows;
    int fd,client,slot,binary_path;
    uint64_t retry,rx,tx,last_rx,ping_time,rtt,keepalive_at,session;
    uint64_t connected_at,tx_enqueued,tx_dequeued,tx_full;
    uint64_t tx_wait_samples,tx_wait_total_ms,tx_wait_max_ms,tx_backlog_since,tx_backlog_peak_ms,tx_stats_reset_at;
    uint64_t opens,frames_sent,frame_bytes_sent,frames_received,frame_bytes_received;
    size_t tx_peak,tx_memory_peak,rx_peak;
    time_t auth_expires;
    vr_decoder decoder;
    /* Invalidated on every policy publication, including removed path edges. */
    uint8_t ingress_control[1024];char ingress_path[VR_PATH_HOPS+1][129];
    size_t ingress_length,ingress_count,ingress_at;
    /* Immutable frames, including SSL_write WANT retries; memory is bounded. */
    vr_tx_frame *tx_head[3],*tx_tail[3],*writing;
    size_t used,tx_memory,tx_memory_class[3],tx_bytes[3],write_offset,write_pending;
    unsigned business_since_bulk,next_flow_slot;
    short write_events;
} vr_link;
typedef struct {vr_engine *engine;pthread_t thread;int started,done,fd,index;vr_link *result;} vr_handshake;
typedef struct {uint32_t hash;int link;uint64_t session,used;unsigned revision;uint8_t nodes[VR_PATH_HOPS+1],count;} vr_flow_binding;
/* Bounded hot packet paths, only after full owner/path authorization. */
#define VR_PACKET_CACHE_MAX 16
typedef struct {
    uint8_t key[13],nodes[VR_PATH_HOPS+1],count,control[128];
    size_t control_len;int link,binding;unsigned revision;uint32_t hash;uint64_t session;
} vr_packet_cache;

#define VR_PATH_RTT_MAX 64
/* Node indexes are valid only within policy_revision; no strings or JFFS writes. */
typedef struct {uint8_t nodes[VR_PATH_HOPS+1],count;char id[49];uint64_t rtt,updated,sent,used;} vr_path_rtt;
typedef struct {char domain[254],ip[16],rule[129],exit[129];time_t expires;uint64_t used;uint32_t resolved;uint8_t local_exit,dns_blocked;} vr_mapping;
#define VR_DNS_BLOCKED_CACHE_MAX 128
#define VR_MAPPING_TTL (48*60*60)
/* Atomic replacement can temporarily retain both the old and new file. */
#define VR_MAPPING_FILE_MAX ((1024u*1024u-1u)/2u)
typedef struct {char key[65];int busy;uint64_t retry,used;} vr_mapping_pending;
typedef struct {int state,incoming;char ip[16],path[VR_PATH_MAX][129];size_t n,count;uint8_t packet[1500];} vr_resolve_job;
typedef struct {vr_packet original,wire;int entry,tcp_syn;uint32_t tcp_sequence;uint64_t used;char target[129];} vr_nat;
typedef struct {int tcp;size_t n;uint8_t query[4096];struct sockaddr_in source;socklen_t source_len;} vr_dns_job;
typedef struct {char id[129],path[VR_PATH_MAX][129];size_t count;uint64_t first,last,bytes,frames,limit,deadline;json_object *message;} vr_speed_rx;
struct vr_engine {
    vr_service *service;vr_config config;unsigned generation;
    vr_observation observation;
    vr_addresses addresses;
    json_object *snapshot,*policy,*next_policy;
    unsigned policy_revision;
    uint64_t policy_retry;
    uint64_t auth_reject_log;
    char own_ip[16],cidr[64],error[256];
    SSL_CTX *tls;
    vr_link links[VR_CARRIER_MAX];size_t link_count;
    vr_flow_binding bindings[VR_NAT_MAX];uint64_t carrier_session;
    vr_packet_cache packet_cache[VR_PACKET_CACHE_MAX];
    vr_path_rtt path_rtt[VR_PATH_RTT_MAX];unsigned paths_revision;uint64_t path_probe_at;
    vr_handshake handshakes[2];
    pthread_t control_thread;int control_started,control_stop,controller_connected,refresh;
    char controller_endpoint[280];int controller_direct;
    /* Immutable main-thread snapshot; readers copy it under mutex. */
    json_object *telemetry;
    char stats_started_at[40],public_ipv4[16],public_ipv6[46];
    unsigned buffers_reset,telemetry_epoch;
    pthread_t public_ip_thread;int public_ip_started;
    vr_monitor *monitor;
    int tun,lan,wan,dns_udp,dns_tcp,platform_started,carrier6;
    struct vr_tun_reader *tun_reader;
    struct vr_tun_writer *tun_writer;
    char carrier6_iface[16];
    pthread_mutex_t mutex;pthread_cond_t jobs_changed;
    vr_mapping mappings[VR_MAPPING_MAX];
    uint32_t dns_blocked_ips[VR_DNS_BLOCKED_CACHE_MAX];
    uint32_t dns_blocked_overflow[(VR_MAPPING_MAX+31)/32];
    size_t dns_blocked_count;
    vr_mapping_pending mapping_pending[16];
    char mapping_path[512],mapping_scope[65];
    unsigned mapping_dirty;uint64_t mapping_flush_at;
    vr_nat flows[VR_NAT_MAX];uint16_t next_port;
    struct vr_external_state *external;
    struct vr_tcp_state *tcp;
    struct vr_dns_filter_state *dns_filter;
    vr_resolve_job resolves[16];pthread_t resolve_thread;
    pthread_cond_t resolve_changed;int resolve_started,resolve_stop;
    vr_dns_job jobs[16];size_t job_head,job_count;
    int dns_stop;pthread_t dns_workers[4];size_t dns_worker_count;
    vr_speed_rx receives[4];
    json_object *speed_send;
    char speed_path[VR_PATH_MAX][129],measure_id[49];size_t speed_count;
    uint64_t speed_started,speed_bytes,speed_limit,speed_duration,measure_started,measure_timeout;
};

static inline uint64_t vr_observe_begin(vr_engine *e,unsigned stage){return e->observation.active?vr_observe_sample(e,stage):0;}

/* Workers use a value snapshot: address refresh never mutates their arguments. */
static inline vr_addresses vr_engine_addresses(vr_engine *e)
{
    vr_addresses a;pthread_mutex_lock(&e->mutex);a=e->addresses;pthread_mutex_unlock(&e->mutex);return a;
}
/* Main thread only; preserve all established streams on success and failure. */
int vr_addresses_apply(vr_engine *e,const vr_addresses *next);
int vr_path(vr_engine *e,const char *target,char path[VR_PATH_MAX][129],size_t *n);
int vr_packet_path(vr_engine *e,const char *target,const uint8_t *packet,size_t length,char path[VR_PATH_MAX][129],size_t *n);
int vr_send(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,const void *data,size_t len);
int vr_send_json(vr_engine *e,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,json_object *message);
int vr_link_send_json(vr_link *link,uint16_t main,uint16_t sub,char path[VR_PATH_MAX][129],size_t n,json_object *message);
void vr_link_close(vr_link *l,const char *error);
int vr_link_queue(vr_link *l,const void *bytes,size_t n);
int vr_link_keepalive(vr_link *l,uint16_t subtype);
uint64_t vr_link_oldest_wait_ms(vr_link *l,uint64_t now);
void vr_link_dial(vr_link *l);
int vr_carrier_slot(const char *header);
int vr_links_add(vr_engine *e,json_object *rule);
void vr_links_reconcile(vr_engine *e,json_object *policy);
/* Main thread only: 0 unchanged/deferred, 1 applied, 2 requires engine rebuild. */
int vr_policy_apply_pending(vr_engine *e);
vr_link *vr_carrier_select(vr_engine *e,const char *peer,const vr_frame *frame);
json_object *vr_links_status(vr_engine *e);
void vr_link_accept(vr_engine *e,int fd);
void vr_links_close_failed(vr_link *links, size_t count, const struct pollfd *fds);
void vr_links_poll(vr_link *links, size_t count, const struct pollfd *fds);
int vr_link_receive(const vr_frame *frame,void *link);
void vr_handshakes_poll(vr_engine *e,int finish);
int vr_control_start(vr_engine *e);
void vr_control_stop(vr_engine *e);
void vr_telemetry_publish(vr_engine *e);
void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count);
int vr_mapping_get(vr_engine *e,const char *domain,const char *ip,vr_decision decision,vr_mapping *out);
int vr_mapping_lookup(vr_engine *e,const char *domain,const char *ip,vr_decision decision,vr_mapping *out);
void vr_mapping_load(vr_engine *e);
int vr_mapping_flush(vr_engine *e,int force);
/* Caller holds e->mutex; retain TTL while reconciling rule/exit changes. */
void vr_mapping_reconcile(vr_engine *e);
/* Caller holds engine mutex (or startup, before workers). RAM-only flags. */
void vr_mapping_dns_update(vr_engine *e,vr_mapping *mapping);
void vr_mapping_dns_refresh(vr_engine *e);
int vr_mapping_dns_blocked(vr_engine *e,uint32_t ip,size_t *scanned);
/* Caller holds engine mutex. Local-only mappings still follow current rules. */
vr_decision vr_mapping_decision(vr_engine *e,const vr_mapping *mapping);
void vr_outgoing(vr_engine *e,uint8_t *bytes,size_t n);
void vr_tun_drain(vr_engine *e);
int vr_tun_reader_start(vr_engine *e);
int vr_tun_reader_poll_fd(vr_engine *e);
json_object *vr_tun_reader_status(vr_engine *e);
void vr_tun_reader_stop(vr_engine *e);
int vr_tun_writer_start(vr_engine *e);
void vr_tun_write(vr_engine *e,const void *bytes,size_t length);
void vr_tun_writer_flush(vr_engine *e);
json_object *vr_tun_writer_status(vr_engine *e);
void vr_tun_writer_stop(vr_engine *e);
int vr_dataplane_start(vr_engine *e);
void vr_dataplane_tick(vr_engine *e);
void vr_dataplane_stop(vr_engine *e);
void vr_incoming(vr_engine *e,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count);
int vr_dns_start(vr_engine *e);
void vr_dns_accept(vr_engine *e,int tcp);
void vr_dns_stop(vr_engine *e);
void vr_dns_filter_tick(vr_engine *e);
void vr_dns_filter_stop(vr_engine *e);
void vr_measure_frame(vr_engine *e,vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n);
void vr_measure_tick(vr_engine *e);
void vr_measure_stop(vr_engine *e);
int vr_tcp_start(vr_engine *e);
int vr_tcp_listener_active(vr_engine *e);
json_object *vr_tcp_sni_cache_status(vr_engine *e);
void vr_tcp_listener_replace(vr_engine *e,int fd);
int vr_tcp_attach(vr_engine *e,int fd,const char *ip,int port);
size_t vr_tcp_pollfds(vr_engine *e,struct pollfd *fds);
void vr_tcp_window_budget_tick(vr_engine *e,uint64_t now);
int vr_external_tcp_window_locked(vr_engine *e,uint64_t now,vr_tcp_window_socket *socket);
void vr_tcp_poll(vr_engine *e,struct pollfd *fds);
void vr_tcp_frame(vr_engine *e,const vr_frame *f,char path[VR_PATH_MAX][129],size_t count);
void vr_tcp_stop(vr_engine *e);
int vr_external_start(vr_engine *e,const char *node,int http);
void vr_external_tick(vr_engine *e);
void vr_external_stop(vr_engine *e);
int vr_external_target(vr_engine *e,const vr_packet *p,uint32_t *real,char node[129]);
#endif
