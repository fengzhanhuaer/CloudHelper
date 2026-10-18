#ifndef CHVR_H
#define CHVR_H

#include <stddef.h>
#include <stdint.h>
#include <time.h>
#include <json-c/json.h>

#define VR_KIND "asus_56uv2"
#ifndef VR_VERSION
#define VR_VERSION "0.0.0-dev"
#endif
#define VR_CONTROL_MAX 8096u
#define VR_DATA_MAX 65535u
#define VR_FRAME_MAX (12u + VR_CONTROL_MAX + VR_DATA_MAX)
#define VR_JSON_MAX (2u * 1024u * 1024u)
#define VR_LINK_MAX 8
#define VR_CARRIER_SLOTS 4
#define VR_CARRIER_MAX (VR_LINK_MAX * VR_CARRIER_SLOTS)
#define VR_ROUTE_MAX 2048
#define VR_MAPPING_MAX 1024
#define VR_PATH_MAX 16
#define VR_PATH_HOPS 3
#define VR_RELAY_PATH "/api/node/route/relay"
#define VR_GATEWAY_CIDRS 32
#define VR_GATEWAY_NODES 64

typedef struct {
    int enabled;
    size_t cidr_count, node_count;
    char cidrs[VR_GATEWAY_CIDRS][19], nodes[VR_GATEWAY_NODES][129];
} vr_gateway;

typedef struct {
    int enabled, allow_wan, dns_enabled, port;
    int doh_enabled,dot_enabled,plain_dns_enabled;
    char controller[1024], node_id[129], node_key[1025], dns_upstream[64], dns_doh[1024], dns_dot[260];
    vr_gateway gateway;
} vr_config;

typedef struct {
    uint16_t main, sub;
    const uint8_t *control, *data;
    size_t control_len, data_len;
} vr_frame;

typedef struct {
    uint8_t bytes[VR_FRAME_MAX];
    size_t used, needed;
} vr_decoder;

typedef int (*vr_frame_handler)(const vr_frame *, void *);

typedef struct {
    char name[254];
    uint16_t id, type;
    size_t end;
} vr_question;

typedef enum { VR_DIRECT, VR_REJECT, VR_PROXY } vr_action;
typedef struct { vr_action action; char rule[129], exit_node[129], backup[129]; } vr_decision;

const char *vr_string(json_object *o, const char *key);
int vr_controller_auth(const vr_config *c,const char *iface,const char *method,const char *path,char challenge[513],char signature[65],char *err,size_t n);
int vr_timestamp(const char *s,time_t *out);
int vr_dns_address4(const uint8_t *reply,size_t n,char out[16]);
int vr_dns_addresses4(const uint8_t *reply,size_t n,char out[4][16]);
int vr_policy_needs_ip(json_object *policy,const char *domain);
json_object *vr_member(json_object *o, const char *key);
json_object *vr_json_parse(const void *data, size_t n);
int vr_copy(char *dst, size_t capacity, const char *src);
int vr_origin(const char *url, char host[254], int *port, int *tls);
int vr_atomic_write(const char *path, const void *bytes, size_t n);
int vr_read_file(const char *path, void *bytes, size_t capacity, size_t *n);
void vr_config_default(vr_config *c);
int vr_config_parse(vr_config *out, json_object *o, const vr_config *previous, char *err, size_t n);
int vr_config_load(vr_config *c, const char *path, char *err, size_t n);
int vr_config_save(const vr_config *c, const char *path);
json_object *vr_config_json(const vr_config *c, int include_secret);
int vr_gateway_parse(vr_gateway *g, json_object *o);
json_object *vr_gateway_json(const vr_gateway *g);
int vr_gateway_contains(const vr_gateway *g, const char *ip);
int vr_gateway_allowed(const vr_gateway *g, const char *node);
int vr_private_cidr(const char *raw, char normalized[19]);
int vr_cidr_contains(const char *raw, const char *ip);

uint16_t vr_checksum(const void *bytes, size_t n);
int vr_frame_encode(const vr_frame *frame, uint8_t *dst, size_t cap, size_t *n);
int vr_decoder_feed(vr_decoder *d, const void *bytes, size_t n, vr_frame_handler fn, void *arg);
int vr_frame_path(const vr_frame *frame, char path[VR_PATH_MAX][129], size_t *n);

int vr_dns_question(const uint8_t *wire, size_t n, vr_question *q);
int vr_dns_reply(const uint8_t *query, size_t qn, uint8_t *out, size_t cap,
                 int rcode, const char *ipv4, size_t *n);
int vr_dns_reply_ttl(const uint8_t *query,size_t qn,uint8_t *out,size_t cap,
                     int rcode,const char *ipv4,uint32_t ttl,size_t *n);
int vr_dns_exchange(const char *upstream, const char *iface, const void *query, size_t qn,
                    void *reply, size_t cap, size_t *n, int tcp);
int vr_dns_endpoint(const char *text, char host[254], int *port, int literal);
int vr_dns_doh_endpoint(const char *text, char host[254], int *port);
int vr_dns_chain(const vr_config *config,const char *iface,const void *query,size_t qn,void *reply,size_t cap,size_t *n);
int vr_resolve_config4(const char *host,const vr_config *config,const char *iface,char out[16]);
int vr_resolve_config4_all(const char *host,const vr_config *config,const char *iface,char out[4][16]);
int vr_resolve4(const char *host, const char *upstream, const char *iface, char out[16]);
vr_decision vr_policy_match(json_object *policy, const char *domain, const char *ip);
int vr_policy_validate(json_object *snapshot, const char *node_id, char *err, size_t n);
int vr_policy_service_port(json_object *policy,const char *node_id,int fallback);
int vr_policy_equal(json_object *a,json_object *b);
json_object *vr_cache_load(const char *path);
int vr_cache_save(const char *path,json_object *o);
json_object *vr_policy_fetch_pending(const vr_config *c,const char *iface,const char *path,json_object *owned_pending,char *err,size_t n);
json_object *vr_policy_fetch_cached(const vr_config *c,const char *iface,const char *path,char *err,size_t n);

int vr_random_hex(char *dst, size_t bytes);
int vr_hmac(const char *secret, const char *text, char out[65]);
int vr_route_id(const char *rule, char out[33]);
int vr_ticket_verify(const char *ticket, const char *pubkey, const char *route,
                     const char *from, const char *to, time_t now, time_t *expires);

/* HTTP calls return an owned JSON object; failures never include credentials. */
json_object *vr_controller_call(const vr_config *c, const char *iface,
                               const char *method, const char *path,
                               json_object *body, char *err, size_t n);
/* First issuance follows Probe's eight-minute budget; a usable cached identity
   keeps the ordinary short refresh budget. Cancellation never enters the UI. */
json_object *vr_controller_certificate(const vr_config *c,const char *iface,int cached,
                                     int (*cancelled)(void *),void *arg,char *err,size_t n);

#endif
