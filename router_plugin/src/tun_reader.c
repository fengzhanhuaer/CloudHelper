#define _GNU_SOURCE
#include "engine.h"
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdlib.h>
#include <sys/eventfd.h>
#include <unistd.h>

enum { READER_CAPACITY = 256, READ_BATCH = 16, DRAIN_BATCH = 64,
       PACKET_BYTES = 2048, DRAIN_BUDGET_MS = 4 };

typedef struct {
    size_t length;
    uint8_t bytes[PACKET_BYTES];
} tun_read_packet;

struct vr_tun_reader {
    pthread_t thread;
    pthread_mutex_t mutex;
    pthread_cond_t space;
    int fd, ready, stop, stopping, failed;
    size_t head, count, peak;
    uint64_t received, full_events;
    tun_read_packet packets[READER_CAPACITY];
};

static void signal_ready(int fd)
{
    while(eventfd_write(fd,1)<0&&errno==EINTR){}
}

static int wait_for_input(struct vr_tun_reader *reader)
{
    struct pollfd fds[]={{reader->fd,POLLIN,0},{reader->stop,POLLIN,0}};
    int rc;
    do rc=poll(fds,2,-1);while(rc<0&&errno==EINTR);
    if(rc<0)return -1;
    if(fds[1].revents)return 0;
    return fds[0].revents&POLLIN?1:-1;
}

static void *tun_reader_main(void *arg)
{
    struct vr_tun_reader *reader=arg;
    int failed=0;
    for(;;) {
        pthread_mutex_lock(&reader->mutex);
        while(reader->count==READER_CAPACITY&&!reader->stopping)
            pthread_cond_wait(&reader->space,&reader->mutex);
        int stopping=reader->stopping;
        size_t tail=(reader->head+reader->count)%READER_CAPACITY;
        size_t available=READER_CAPACITY-reader->count;
        pthread_mutex_unlock(&reader->mutex);
        if(stopping)break;
        int input=wait_for_input(reader);
        if(input<0){failed=1;break;}
        if(!input)break;

        /* Only the reader fills free slots. The engine cannot see them until
           publication, and it only releases occupied slots after forwarding. */
        size_t count=0;
        while(count<available&&count<READ_BATCH) {
            tun_read_packet *packet=&reader->packets[(tail+count)%READER_CAPACITY];
            ssize_t n=read(reader->fd,packet->bytes,sizeof(packet->bytes));
            if(n<0&&errno==EINTR)continue;
            if(n<=0){failed=!n||(errno!=EAGAIN&&errno!=EWOULDBLOCK);break;}
            packet->length=(size_t)n;
            count++;
        }
        if(count) {
            pthread_mutex_lock(&reader->mutex);
            if(!reader->count)signal_ready(reader->ready);
            reader->count+=count;
            reader->received+=count;
            if(reader->count>reader->peak)reader->peak=reader->count;
            if(reader->count==READER_CAPACITY)reader->full_events++;
            pthread_mutex_unlock(&reader->mutex);
        }
        if(failed)break;
    }
    if(failed) {
        pthread_mutex_lock(&reader->mutex);
        reader->failed=1;
        if(!reader->count)signal_ready(reader->ready);
        pthread_mutex_unlock(&reader->mutex);
    }
    return NULL;
}

int vr_tun_reader_start(vr_engine *e)
{
    int flags=fcntl(e->tun,F_GETFL);
    if(flags<0||!(flags&O_NONBLOCK))return -1;
    struct vr_tun_reader *reader=calloc(1,sizeof(*reader));
    if(!reader)return -1;
    reader->fd=e->tun;
    reader->ready=eventfd(0,EFD_NONBLOCK|EFD_CLOEXEC);
    if(reader->ready<0)goto release;
    reader->stop=eventfd(0,EFD_NONBLOCK|EFD_CLOEXEC);
    if(reader->stop<0)goto close_ready;
    if(pthread_mutex_init(&reader->mutex,NULL))goto close_stop;
    if(pthread_cond_init(&reader->space,NULL))goto destroy_mutex;
    pthread_attr_t attr;
    if(pthread_attr_init(&attr))goto destroy_cond;
    int result=pthread_attr_setstacksize(&attr,128*1024);
    if(!result)result=pthread_create(&reader->thread,&attr,tun_reader_main,reader);
    pthread_attr_destroy(&attr);
    if(result)goto destroy_cond;
    e->tun_reader=reader;
    return 0;

destroy_cond:
    pthread_cond_destroy(&reader->space);
destroy_mutex:
    pthread_mutex_destroy(&reader->mutex);
close_stop:
    close(reader->stop);
close_ready:
    close(reader->ready);
release:
    free(reader);
    return -1;
}

int vr_tun_reader_poll_fd(vr_engine *e)
{
    return e->tun_reader?e->tun_reader->ready:e->tun;
}

void vr_tun_drain(vr_engine *e)
{
    struct vr_tun_reader *reader=e->tun_reader;
    uint64_t start=vr_millis();
    if(reader) {
        pthread_mutex_lock(&reader->mutex);
        size_t head=reader->head,count=reader->count;
        int failed=reader->failed;
        pthread_mutex_unlock(&reader->mutex);
        if(!count&&failed) {
            vr_tun_reader_stop(e);
            if(e->service)vr_service_log(e->service,"service","TUN reader stopped; using synchronous reads");
            return;
        }

        size_t processed=0;
        while(processed<count&&processed<DRAIN_BATCH) {
            tun_read_packet *packet=&reader->packets[(head+processed)%READER_CAPACITY];
            vr_outgoing(e,packet->bytes,packet->length);
            processed++;
            if(vr_carrier_tun_paused(e)||vr_millis()-start>=DRAIN_BUDGET_MS)break;
        }
        pthread_mutex_lock(&reader->mutex);
        if(reader->count==READER_CAPACITY&&processed)pthread_cond_signal(&reader->space);
        reader->head=(head+processed)%READER_CAPACITY;
        reader->count-=processed;
        /* Keep the poll notification readable until the whole FIFO is empty. */
        if(!reader->count&&!reader->failed) {
            eventfd_t value;
            while(eventfd_read(reader->ready,&value)<0&&errno==EINTR){}
        }
        pthread_mutex_unlock(&reader->mutex);
        return;
    }

    uint8_t packet[PACKET_BYTES];
    for(unsigned i=0;i<READ_BATCH;i++) {
        ssize_t n=read(e->tun,packet,sizeof(packet));
        if(n<=0)break;
        vr_outgoing(e,packet,(size_t)n);
        if(vr_carrier_tun_paused(e))break;
        if(vr_millis()-start>=DRAIN_BUDGET_MS)break;
    }
}

json_object *vr_tun_reader_status(vr_engine *e)
{
    if(e->tun<0)return NULL;
    json_object *status=json_object_new_object();
    struct vr_tun_reader *reader=e->tun_reader;
    if(reader) {
        pthread_mutex_lock(&reader->mutex);
        size_t queued=reader->count,peak=reader->peak;
        uint64_t read_packets=reader->received,full_events=reader->full_events;
        int failed=reader->failed;
        pthread_mutex_unlock(&reader->mutex);
        json_object_object_add(status,"queued_packets",json_object_new_int((int)queued));
        json_object_object_add(status,"peak_packets",json_object_new_int((int)peak));
        json_object_object_add(status,"capacity_packets",json_object_new_int(READER_CAPACITY));
        json_object_object_add(status,"read_packets",json_object_new_int64((int64_t)read_packets));
        json_object_object_add(status,"full_events",json_object_new_int64((int64_t)full_events));
        json_object_object_add(status,"read_errors",json_object_new_int(failed));
    }
    char drops[32]={0};size_t length;
    if(!vr_read_file("/sys/class/net/chvr0/statistics/tx_dropped",drops,sizeof(drops)-1,&length)&&length)
        json_object_object_add(status,"kernel_tx_dropped",json_object_new_int64(strtoll(drops,NULL,10)));
    return status;
}

void vr_tun_reader_stop(vr_engine *e)
{
    struct vr_tun_reader *reader=e->tun_reader;
    if(!reader)return;
    pthread_mutex_lock(&reader->mutex);
    reader->stopping=1;
    pthread_cond_signal(&reader->space);
    pthread_mutex_unlock(&reader->mutex);
    signal_ready(reader->stop);
    pthread_join(reader->thread,NULL);
    pthread_cond_destroy(&reader->space);
    pthread_mutex_destroy(&reader->mutex);
    close(reader->stop);
    close(reader->ready);
    e->tun_reader=NULL;
    free(reader);
}
