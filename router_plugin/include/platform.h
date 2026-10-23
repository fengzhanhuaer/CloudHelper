#ifndef CHVR_PLATFORM_H
#define CHVR_PLATFORM_H
#include "chvr.h"
typedef struct {char lan[16],wan[16],lan_if[16],wan_if[16];} vr_addresses;
typedef struct {vr_addresses candidate;int pending;} vr_address_watch;
json_object *vr_probe_report(const vr_config *c,const vr_addresses *a,const char *net_root);
int vr_public_ip(const char *text,int family,char *out,size_t cap);
int vr_report_addresses(json_object *report,const char *public4,const char *public6);
int vr_addresses_get(vr_addresses *a);
/* -1: unreadable (keep bindings), 0: unchanged, 1: candidate, 2: confirmed.
 * Call at the normal address-check interval, never twice in one iteration. */
int vr_addresses_check(vr_address_watch *watch,const vr_addresses *current,vr_addresses *next,char *detail,size_t capacity);
int vr_platform_start(const vr_config *c,const vr_addresses *a,const char *ip,const char *cidr);
int vr_platform_readdress(const vr_config *c,const vr_addresses *old,const vr_addresses *next,const char *cidr);
void vr_platform_stop(const char *cidr);
int vr_platform_dns(int enabled);
int vr_platform_routes(json_object *policy);
int vr_platform_routes_update(json_object *previous,json_object *next);
int vr_platform_healthy(void);
int vr_platform_check(char *detail,size_t capacity);
int vr_platform_repair(const vr_config *c,const vr_addresses *a,const char *cidr);
int vr_platform_tls(const vr_addresses *a,const char *cidr,int enabled);
int vr_bind4(const char *ip,int port,int tcp);
int vr_runtime_mount(const char *operation);
#endif
