#ifndef CHVR_NETWORK_MONITOR_H
#define CHVR_NETWORK_MONITOR_H
#include "chvr.h"
typedef struct vr_engine vr_engine;
typedef struct vr_monitor vr_monitor;
int vr_monitor_start(vr_engine *engine);
void vr_monitor_stop(vr_engine *engine);
void vr_monitor_submit(vr_engine *engine,json_object *message);
/* Ownership of the serialized newline-terminated result passes to caller. */
char *vr_monitor_take(vr_engine *engine);
#endif
