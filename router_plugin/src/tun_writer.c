#define _POSIX_C_SOURCE 200809L
#include "engine.h"
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

enum { TUN_QUEUE_PACKETS = 64, TUN_HIGH_WATER = 48, TUN_WRITE_BATCH = 16, TUN_PACKET_BYTES = 2048 };

typedef struct {
    size_t length;
    unsigned observation_generation;
    uint8_t bytes[TUN_PACKET_BYTES];
} tun_packet;

struct vr_tun_writer {
    pthread_t thread;
    pthread_mutex_t mutex;
    pthread_cond_t readable, writable;
    int fd, stopping;
    size_t head, count, peak;
    uint64_t written, errors, full_events, reported_errors;
    unsigned observation_generation;
    vr_observe_stage observed;
    tun_packet packets[TUN_QUEUE_PACKETS];
};

static int write_packet(int fd,const void *bytes,size_t length)
{
    ssize_t result;
    do result=write(fd,bytes,length);while(result<0&&errno==EINTR);
    return result==(ssize_t)length;
}

static void *tun_writer_main(void *arg)
{
    struct vr_tun_writer *writer=arg;
    pthread_mutex_lock(&writer->mutex);
    for(;;) {
        while(!writer->count&&!writer->stopping)pthread_cond_wait(&writer->readable,&writer->mutex);
        if(!writer->count)break;
        size_t head=writer->head;
        size_t count=writer->count<TUN_WRITE_BATCH?writer->count:TUN_WRITE_BATCH;
        pthread_mutex_unlock(&writer->mutex);

        /* These slots remain occupied until the writes complete. The producer
           may append but cannot reuse them while the kernel takes the packet. */
        int success[TUN_WRITE_BATCH];
        uint64_t elapsed[TUN_WRITE_BATCH]={0};
        for(size_t i=0;i<count;i++) {
            const tun_packet *packet=&writer->packets[(head+i)%TUN_QUEUE_PACKETS];
            uint64_t start=packet->observation_generation?vr_observe_clock_ns():0;
            success[i]=write_packet(writer->fd,packet->bytes,packet->length);
            if(start) {
                uint64_t end=vr_observe_clock_ns();
                if(end>start)elapsed[i]=end-start;
            }
        }

        pthread_mutex_lock(&writer->mutex);
        for(size_t i=0;i<count;i++) {
            if(success[i])writer->written++;else writer->errors++;
            const tun_packet *packet=&writer->packets[(head+i)%TUN_QUEUE_PACKETS];
            if(!elapsed[i]||packet->observation_generation!=writer->observation_generation)continue;
            vr_observe_stage *stats=&writer->observed;
            uint64_t duration=elapsed[i];
            stats->samples++;stats->total_ns+=duration;stats->hits+=success[i];
            if(duration>stats->max_ns)stats->max_ns=duration;
        }
        writer->head=(head+count)%TUN_QUEUE_PACKETS;
        writer->count-=count;
        pthread_cond_signal(&writer->writable);
    }
    pthread_mutex_unlock(&writer->mutex);
    return NULL;
}

int vr_tun_writer_start(vr_engine *e)
{
    struct vr_tun_writer *writer=calloc(1,sizeof(*writer));
    if(!writer)return -1;
    writer->fd=e->tun;
    if(pthread_mutex_init(&writer->mutex,NULL))goto free_writer;
    if(pthread_cond_init(&writer->readable,NULL))goto destroy_mutex;
    if(pthread_cond_init(&writer->writable,NULL))goto destroy_readable;
    pthread_attr_t attr;
    if(pthread_attr_init(&attr))goto destroy_writable;
    int result=pthread_attr_setstacksize(&attr,128*1024);
    if(!result)result=pthread_create(&writer->thread,&attr,tun_writer_main,writer);
    pthread_attr_destroy(&attr);
    if(result)goto destroy_writable;
    e->tun_writer=writer;
    return 0;

destroy_writable:
    pthread_cond_destroy(&writer->writable);
destroy_readable:
    pthread_cond_destroy(&writer->readable);
destroy_mutex:
    pthread_mutex_destroy(&writer->mutex);
free_writer:
    free(writer);
    return -1;
}

int vr_tun_write(vr_engine *e,const void *bytes,size_t length)
{
    struct vr_tun_writer *writer=e->tun_writer;
    if(!writer) {
        uint64_t observed=vr_observe_begin(e,VR_OBS_TUN_WRITE);
        int success=write_packet(e->tun,bytes,length);
        vr_observe_end(e,VR_OBS_TUN_WRITE,observed,0,success);
        if(!success)snprintf(e->error,sizeof(e->error),"TUN receive queue unavailable");
        return 0;
    }
    if(length>TUN_PACKET_BYTES) {
        snprintf(e->error,sizeof(e->error),"TUN packet exceeds write buffer");
        return 0;
    }

    vr_observation *observation=&e->observation;
    unsigned generation=0;
    if(observation->active&&++observation->stages[VR_OBS_TUN_WRITE].calls%VR_OBSERVE_INTERVAL==1)
        generation=observation->generation;
    pthread_mutex_lock(&writer->mutex);
    int pressure=writer->count>=TUN_HIGH_WATER;
    if(writer->count==TUN_QUEUE_PACKETS) {
        writer->full_events++;
        /* The worker uses the same nonblocking TUN descriptor as the old
           synchronous path. Waiting here keeps the queue bounded in bursts. */
        do pthread_cond_wait(&writer->writable,&writer->mutex);while(writer->count==TUN_QUEUE_PACKETS);
    }
    if(generation&&generation!=writer->observation_generation) {
        writer->observation_generation=generation;
        memset(&writer->observed,0,sizeof(writer->observed));
    }
    tun_packet *packet=&writer->packets[(writer->head+writer->count)%TUN_QUEUE_PACKETS];
    memcpy(packet->bytes,bytes,length);
    packet->length=length;
    packet->observation_generation=generation;
    if(writer->count++==0)pthread_cond_signal(&writer->readable);
    if(writer->count>writer->peak)writer->peak=writer->count;
    pthread_mutex_unlock(&writer->mutex);
    return pressure;
}

void vr_tun_writer_flush(vr_engine *e)
{
    struct vr_tun_writer *writer=e->tun_writer;
    if(!writer)return;
    pthread_mutex_lock(&writer->mutex);
    while(writer->count)pthread_cond_wait(&writer->writable,&writer->mutex);
    pthread_mutex_unlock(&writer->mutex);
}

json_object *vr_tun_writer_status(vr_engine *e)
{
    struct vr_tun_writer *writer=e->tun_writer;
    if(!writer)return NULL;
    pthread_mutex_lock(&writer->mutex);
    size_t queued=writer->count,peak=writer->peak;
    uint64_t written=writer->written,errors=writer->errors,full=writer->full_events;
    if(errors!=writer->reported_errors) {
        writer->reported_errors=errors;
        snprintf(e->error,sizeof(e->error),"TUN receive queue unavailable");
    }
    if(writer->observation_generation==e->observation.generation) {
        vr_observe_stage *stage=&e->observation.stages[VR_OBS_TUN_WRITE];
        uint64_t calls=stage->calls;
        *stage=writer->observed;
        stage->calls=calls;
    }
    pthread_mutex_unlock(&writer->mutex);
    json_object *status=json_object_new_object();
    json_object_object_add(status,"queued_packets",json_object_new_int((int)queued));
    json_object_object_add(status,"peak_packets",json_object_new_int((int)peak));
    json_object_object_add(status,"capacity_packets",json_object_new_int(TUN_QUEUE_PACKETS));
    json_object_object_add(status,"written_packets",json_object_new_int64((int64_t)written));
    json_object_object_add(status,"full_events",json_object_new_int64((int64_t)full));
    json_object_object_add(status,"write_errors",json_object_new_int64((int64_t)errors));
    return status;
}

void vr_tun_writer_stop(vr_engine *e)
{
    struct vr_tun_writer *writer=e->tun_writer;
    if(!writer)return;
    pthread_mutex_lock(&writer->mutex);
    writer->stopping=1;
    pthread_cond_signal(&writer->readable);
    pthread_mutex_unlock(&writer->mutex);
    pthread_join(writer->thread,NULL);
    pthread_cond_destroy(&writer->writable);
    pthread_cond_destroy(&writer->readable);
    pthread_mutex_destroy(&writer->mutex);
    e->tun_writer=NULL;
    free(writer);
}
