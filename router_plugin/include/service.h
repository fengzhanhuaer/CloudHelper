#ifndef CHVR_SERVICE_H
#define CHVR_SERVICE_H
#include "chvr.h"
#include <pthread.h>
#define VR_LOG_MAX 64
typedef struct {time_t at;unsigned repeats;char component[16],message[240];} vr_log_entry;

typedef struct {
    pthread_mutex_t mutex;
    pthread_cond_t changed;
    vr_config config;
    unsigned generation;
    int stop;
    char config_path[512],csrf[49];
    json_object *status,*measurement;
    char measure_node[129],measure_kind[16];
    vr_log_entry logs[VR_LOG_MAX];size_t log_head,log_count;
} vr_service;

int vr_service_stopped(vr_service *s,unsigned generation);
void vr_service_log(vr_service *s,const char *component,const char *message);
json_object *vr_service_logs(vr_service *s);
void vr_service_status(vr_service *s,json_object *owned_status,unsigned generation);
void vr_engine_run(vr_service *service,const vr_config *config,unsigned generation);
int vr_service_run(const char *config_path,const char *socket_path,int cleanup);

#endif
