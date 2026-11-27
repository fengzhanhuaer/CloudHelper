#define _POSIX_C_SOURCE 200809L
#include "engine.h"
#include <netinet/tcp.h>
#include <string.h>
#include <sys/socket.h>
#include <time.h>

static uint64_t nanos(void){struct timespec t;if(clock_gettime(CLOCK_MONOTONIC,&t))return 0;return (uint64_t)t.tv_sec*1000000000u+(uint64_t)t.tv_nsec;}
int vr_observe_request(vr_service *s,unsigned seconds)
{
    if(seconds>60)return -1;
    pthread_mutex_lock(&s->mutex);s->observe_generation++;s->observe_until=seconds?vr_millis()+(uint64_t)seconds*1000:0;pthread_mutex_unlock(&s->mutex);return 0;
}
void vr_observe_refresh(vr_engine *e,uint64_t now)
{
    vr_observation *o=&e->observation;if(o->active&&now>=o->until)o->active=0;
    if(now<o->next_check)return;
    o->next_check=now+100;
    pthread_mutex_lock(&e->service->mutex);unsigned generation=e->service->observe_generation;uint64_t until=e->service->observe_until;pthread_mutex_unlock(&e->service->mutex);
    if(generation==o->generation)return;
    o->generation=generation;o->until=until;o->active=until>now;
    if(o->active)memset(o->stages,0,sizeof(o->stages));
}
uint64_t vr_observe_sample(vr_engine *e,unsigned stage)
{
    if(stage>=VR_OBS_COUNT)return 0;
    vr_observe_stage *s=&e->observation.stages[stage];s->calls++;return (s->calls&63u)==1?nanos():0;
}
void vr_observe_end(vr_engine *e,unsigned stage,uint64_t token,uint64_t steps,int hit)
{
    if(!token||stage>=VR_OBS_COUNT)return;
    uint64_t now=nanos();if(now<token)return;vr_observe_stage *s=&e->observation.stages[stage];uint64_t elapsed=now-token;
    s->samples++;s->total_ns+=elapsed;if(elapsed>s->max_ns)s->max_ns=elapsed;s->scan_steps+=steps;if(hit)s->hits++;
}
json_object *vr_observe_report(vr_engine *e,uint64_t now)
{
    vr_observation *o=&e->observation;if(!o->generation)return NULL;
    static const char *names[]={"packet_in","packet_out","nat_reverse","dns_blacklist","tun_write","frame_rx"};
    json_object *r=json_object_new_object(),*stages=json_object_new_object();
    json_object_object_add(r,"active",json_object_new_boolean(o->active));json_object_object_add(r,"remaining_ms",json_object_new_int64(o->active&&o->until>now?(int64_t)(o->until-now):0));
    json_object_object_add(r,"sample_every",json_object_new_int(64));
    for(unsigned i=0;i<VR_OBS_COUNT;i++){
        vr_observe_stage *s=&o->stages[i];json_object *v=json_object_new_object();
        json_object_object_add(v,"calls",json_object_new_int64((int64_t)s->calls));json_object_object_add(v,"samples",json_object_new_int64((int64_t)s->samples));
        json_object_object_add(v,"average_us",s->samples?json_object_new_double((double)s->total_ns/(double)s->samples/1000):NULL);
        json_object_object_add(v,"max_us",s->samples?json_object_new_double((double)s->max_ns/1000):NULL);
        json_object_object_add(v,"sampled_scan_steps",json_object_new_int64((int64_t)s->scan_steps));json_object_object_add(v,"sampled_hits",json_object_new_int64((int64_t)s->hits));
        json_object_object_add(stages,names[i],v);
    }
    json_object_object_add(r,"stages",stages);
    json_object_object_add(r,"timing",json_object_new_string("sampled_wall_time"));
    json_object *carriers=json_object_new_array();
    for(size_t i=0;i<e->link_count;i++){
        vr_link *l=&e->links[i];if(!l->ssl||l->fd<0)continue;
        json_object *v=json_object_new_object();json_object_object_add(v,"peer",json_object_new_string(l->peer));json_object_object_add(v,"slot",json_object_new_int(l->slot));
        json_object_object_add(v,"tx_bytes",json_object_new_int64((int64_t)l->tx));json_object_object_add(v,"rx_bytes",json_object_new_int64((int64_t)l->rx));
        json_object_object_add(v,"tcp_info",vr_socket_tcp_info(l->fd));json_object_array_add(carriers,v);
    }
    json_object_object_add(r,"carriers",carriers);json_object_object_add(r,"carrier_snapshot_at_ms",json_object_new_int64((int64_t)now));return r;
}
static void field(json_object *r,const char *name,const uint8_t *bytes,size_t length,size_t offset,int millis)
{
    uint32_t value=0;if(length<offset+4){json_object_object_add(r,name,NULL);return;}memcpy(&value,bytes+offset,4);
    json_object_object_add(r,name,millis?json_object_new_double((double)value/1000):json_object_new_int64(value));
}
json_object *vr_tcp_info_decode(const uint8_t *b,size_t n)
{
    json_object *r=json_object_new_object();json_object_object_add(r,"supported",json_object_new_boolean(n>=8));json_object_object_add(r,"kernel_bytes",json_object_new_int64((int64_t)n));
    if(n>=8){json_object_object_add(r,"state",json_object_new_int(b[0]));json_object_object_add(r,"snd_scale",json_object_new_int(b[6]&15));json_object_object_add(r,"rcv_scale",json_object_new_int(b[6]>>4));}
    field(r,"rto_ms",b,n,8,1);field(r,"snd_mss",b,n,16,0);field(r,"rcv_mss",b,n,20,0);field(r,"unacked",b,n,24,0);field(r,"rtt_ms",b,n,68,1);field(r,"rtt_var_ms",b,n,72,1);
    field(r,"ssthresh_packets",b,n,76,0);field(r,"cwnd_packets",b,n,80,0);field(r,"rcv_space",b,n,96,0);field(r,"retransmits",b,n,100,0);
    return r;
}
json_object *vr_socket_tcp_info(int fd)
{
    uint8_t bytes[256]={0};socklen_t n=sizeof(bytes);if(fd<0||getsockopt(fd,IPPROTO_TCP,TCP_INFO,bytes,&n))return vr_tcp_info_decode(bytes,0);return vr_tcp_info_decode(bytes,n);
}
