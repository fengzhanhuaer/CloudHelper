#ifndef CHVR_SERVICE_H
#define CHVR_SERVICE_H
#include "chvr.h"
#include <pthread.h>
#define VR_LOG_MAX 64
typedef struct {time_t at;unsigned repeats;char component[16],message[240];} vr_log_entry;
typedef char vr_log_budget_check[(sizeof(vr_log_entry)*VR_LOG_MAX<256u*1024u)?1:-1];
#define VR_AUTH_IP_MAX 64
#define VR_AUTH_IP_FAILURES 5
#define VR_AUTH_IP_WINDOW_MS 3600000u
typedef struct {vr_ip ip;uint32_t failed_at[VR_AUTH_IP_FAILURES];unsigned char count,next,blocked;} vr_auth_ip;

typedef struct {
    pthread_mutex_t mutex;
    pthread_cond_t changed;
    vr_config config;
    unsigned generation;
    int stop;
    char config_path[512],csrf[49];
    json_object *status,*measurement,*pending_policy,*upgrade;
    pthread_t upgrade_thread;
    int upgrade_joinable;
    unsigned pending_generation;
    char measure_node[129],measure_kind[16];
    vr_log_entry logs[VR_LOG_MAX];size_t log_head,log_count;
    vr_auth_ip auth_ips[VR_AUTH_IP_MAX];
    unsigned auth_ips_revision,auth_ips_saved;
    uint64_t auth_ips_flush_at;
} vr_service;

int vr_service_stopped(vr_service *s,unsigned generation);
int vr_service_ip_blocked(vr_service *s,const vr_ip *ip,uint64_t now);
void vr_service_ip_result(vr_service *s,const vr_ip *ip,int success,uint64_t now);
void vr_service_ip_unblock(vr_service *s,const vr_ip *ip);
json_object *vr_service_ip_status(vr_service *s,uint64_t now);
int vr_service_ip_load(vr_service *s);
int vr_service_ip_flush(vr_service *s,uint64_t now,int force);
void vr_service_log(vr_service *s,const char *component,const char *message);
json_object *vr_service_logs(vr_service *s);
void vr_service_status(vr_service *s,json_object *owned_status,unsigned generation);
void vr_engine_run(vr_service *service,const vr_config *config,unsigned generation);
int vr_service_run(const char *config_path,const char *socket_path,int cleanup);

#endif
