#ifndef CHVR_OBSERVE_H
#define CHVR_OBSERVE_H
#include "service.h"
struct vr_engine;
enum {VR_OBS_PACKET_IN,VR_OBS_PACKET_OUT,VR_OBS_NAT_REVERSE,VR_OBS_DNS_BLACKLIST,VR_OBS_TUN_WRITE,VR_OBS_FRAME_RX,VR_OBS_COUNT};
enum { VR_OBSERVE_INTERVAL = 64 };
typedef struct {uint64_t calls,samples,total_ns,max_ns,scan_steps,hits;} vr_observe_stage;
typedef struct {int active;unsigned generation;uint64_t until,next_check;vr_observe_stage stages[VR_OBS_COUNT];} vr_observation;
int vr_observe_request(vr_service *s,unsigned seconds);
void vr_observe_refresh(struct vr_engine *e,uint64_t now);
uint64_t vr_observe_clock_ns(void);
uint64_t vr_observe_sample(struct vr_engine *e,unsigned stage);
void vr_observe_end(struct vr_engine *e,unsigned stage,uint64_t token,uint64_t steps,int hit);
json_object *vr_observe_report(struct vr_engine *e,uint64_t now);
json_object *vr_tcp_info_decode(const uint8_t *bytes,size_t length);
json_object *vr_socket_tcp_info(int fd);
#endif
