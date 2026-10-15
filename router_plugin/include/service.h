#ifndef CHVR_SERVICE_H
#define CHVR_SERVICE_H
#include "chvr.h"
#include <pthread.h>

typedef struct {
    pthread_mutex_t mutex;
    pthread_cond_t changed;
    vr_config config;
    unsigned generation;
    int stop;
    char config_path[512],csrf[49];
    json_object *status,*measurement;
    char measure_node[129],measure_kind[16];
} vr_service;

int vr_service_stopped(vr_service *s,unsigned generation);
void vr_service_status(vr_service *s,json_object *owned_status,unsigned generation);
void vr_engine_run(vr_service *service,const vr_config *config,unsigned generation);
int vr_service_run(const char *config_path,const char *socket_path,int cleanup);

#endif
