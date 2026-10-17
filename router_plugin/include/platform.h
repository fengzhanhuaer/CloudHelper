#ifndef CHVR_PLATFORM_H
#define CHVR_PLATFORM_H
#include "chvr.h"
typedef struct {char lan[16],wan[16],lan_if[16],wan_if[16];} vr_addresses;
json_object *vr_probe_report(const vr_config *c,const vr_addresses *a,const char *net_root);
int vr_addresses_get(vr_addresses *a);
int vr_platform_start(const vr_config *c,const vr_addresses *a,const char *ip,const char *cidr);
void vr_platform_stop(const char *cidr);
int vr_platform_dns(int enabled);
int vr_platform_routes(json_object *policy);
int vr_platform_healthy(void);
int vr_bind4(const char *ip,int port,int tcp);
int vr_runtime_mount(const char *operation);
#endif
