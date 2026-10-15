#ifndef CHVR_PACKET_H
#define CHVR_PACKET_H
#include "chvr.h"
typedef struct {uint32_t src,dst;uint16_t sport,dport;uint8_t protocol;size_t ihl,length;} vr_packet;
int vr_packet_parse(const uint8_t *bytes,size_t n,vr_packet *p);
int vr_packet_rewrite(uint8_t *bytes,size_t n,uint32_t src,uint32_t dst,uint16_t sport,uint16_t dport);
#endif
