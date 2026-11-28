#define _POSIX_C_SOURCE 200809L
#include "engine.h"
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

enum {
    TUN_QUEUE_PACKETS = 256,
    TUN_HIGH_WATER = TUN_QUEUE_PACKETS * 3 / 4,
    TUN_WRITE_BATCH = 16,
    TUN_PACKET_BYTES = 2048
};

typedef struct {
    size_t length;
    uint64_t deadline;
    unsigned observation_generation;
    uint8_t bytes[TUN_PACKET_BYTES];
} tun_packet;

struct vr_tun_writer {
    pthread_t thread;
    pthread_mutex_t mutex;
    pthread_cond_t readable, writable;
    int fd, stopping;
    size_t head, count, peak;
    uint64_t written, errors, full_events, queue_dropped, reported_errors;
    unsigned observation_generation;
    vr_observe_stage observed;
    tun_packet packets[TUN_QUEUE_PACKETS];
};

static int write_packet(int fd,const void *bytes,size_t length,uint64_t deadline)
{
    for(;;) {
        ssize_t result=write(fd,bytes,length);
        if(result>=0)return result==(ssize_t)length;
        if(errno!=EINTR&&errno!=EAGAIN&&errno!=EWOULDBLOCK&&errno!=ENOBUFS)return 0;
        uint64_t now=vr_millis();
        if(now>=deadline)return 0;
        struct pollfd fd_ready={fd,POLLOUT,0};
        if(poll(&fd_ready,1,(int)(deadline-now))>0) {
            if(fd_ready.revents&(POLLERR|POLLHUP|POLLNVAL))return 0;
            /* TUN can report writable while its current allocation fails. */
            poll(NULL,0,1);
        }
        if(vr_millis()>=deadline)return 0;
    }
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
            success[i]=write_packet(writer->fd,packet->bytes,packet->length,packet->deadline);
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

static int init_writable_condition(pthread_cond_t *condition)
{
    pthread_condattr_t attr;
    if(pthread_condattr_init(&attr))return -1;
    int result=pthread_condattr_setclock(&attr,CLOCK_MONOTONIC);
    if(!result)result=pthread_cond_init(condition,&attr);
    pthread_condattr_destroy(&attr);
    return result;
}

int vr_tun_writer_start(vr_engine *e)
{
    struct vr_tun_writer *writer=calloc(1,sizeof(*writer));
    if(!writer)return -1;
    writer->fd=e->tun;
    if(pthread_mutex_init(&writer->mutex,NULL))goto free_writer;
    if(pthread_cond_init(&writer->readable,NULL))goto destroy_mutex;
    if(init_writable_condition(&writer->writable))goto destroy_readable;
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
    uint64_t deadline=vr_millis()+VR_QUEUE_WAIT_MS;
    struct vr_tun_writer *writer=e->tun_writer;
    if(!writer) {
        uint64_t observed=vr_observe_begin(e,VR_OBS_TUN_WRITE);
        int success=write_packet(e->tun,bytes,length,deadline);
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
        struct timespec until={(time_t)(deadline/1000),(long)(deadline%1000)*1000000L};
        while(writer->count==TUN_QUEUE_PACKETS)
            if(pthread_cond_timedwait(&writer->writable,&writer->mutex,&until))break;
        if(writer->count==TUN_QUEUE_PACKETS) {
            writer->queue_dropped++;
            pthread_mutex_unlock(&writer->mutex);
            return pressure;
        }
    }
    if(generation&&generation!=writer->observation_generation) {
        writer->observation_generation=generation;
        memset(&writer->observed,0,sizeof(writer->observed));
    }
    tun_packet *packet=&writer->packets[(writer->head+writer->count)%TUN_QUEUE_PACKETS];
    memcpy(packet->bytes,bytes,length);
    packet->length=length;
    packet->deadline=deadline;
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

void vr_tun_writer_buffer_report(vr_engine *e,json_object *items,int reset)
{
    struct vr_tun_writer *writer=e->tun_writer;
    if(!writer)return;
    pthread_mutex_lock(&writer->mutex);
    if(reset) {
        writer->peak=writer->count;
        writer->written=writer->errors=writer->full_events=writer->queue_dropped=writer->reported_errors=0;
    }
    size_t depth=writer->count,peak=writer->peak;
    uint64_t written=writer->written,full=writer->full_events,queue_dropped=writer->queue_dropped,errors=writer->errors;
    pthread_mutex_unlock(&writer->mutex);

    json_object *item=json_object_new_object();
    json_object_object_add(item,"id",json_object_new_string("router.tun.tx"));
    json_object_object_add(item,"stage",json_object_new_string("tun_tx"));
    json_object_object_add(item,"direction",json_object_new_string("tx"));
    json_object_object_add(item,"stats_started_at",json_object_new_string(e->stats_started_at));
    json_object_object_add(item,"depth",json_object_new_int((int)depth));
    json_object_object_add(item,"peak_depth",json_object_new_int((int)peak));
    json_object_object_add(item,"allocated_capacity",json_object_new_int(TUN_QUEUE_PACKETS));
    json_object_object_add(item,"peak_allocated_capacity",json_object_new_int(TUN_QUEUE_PACKETS));
    json_object_object_add(item,"max_capacity",json_object_new_int(TUN_QUEUE_PACKETS));
    json_object_object_add(item,"dequeued",json_object_new_int64((int64_t)(written+errors)));
    json_object_object_add(item,"full_events",json_object_new_int64((int64_t)full));
    json_object_object_add(item,"retry_events",json_object_new_int64((int64_t)full));
    json_object_object_add(item,"retry_recovered",json_object_new_int64((int64_t)(full-queue_dropped)));
    json_object_object_add(item,"dropped",json_object_new_int64((int64_t)(queue_dropped+errors)));
    json_object_array_add(items,item);
}

json_object *vr_tun_writer_status(vr_engine *e)
{
    struct vr_tun_writer *writer=e->tun_writer;
    if(!writer)return NULL;
    pthread_mutex_lock(&writer->mutex);
    size_t queued=writer->count,peak=writer->peak;
    uint64_t written=writer->written,errors=writer->errors,full=writer->full_events,dropped=writer->queue_dropped;
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
    json_object_object_add(status,"queue_dropped",json_object_new_int64((int64_t)dropped));
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
