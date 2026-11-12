#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <errno.h>
#include <net/if.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#define TASKS 32
#define TARGETS 20
#define RESULTS 8
typedef struct {
    char id[129],request[129],targets[TARGETS][46];
    int count,timeout,cycle,length;uint64_t due;unsigned revision;
} monitor_job;
struct vr_monitor {
    vr_engine *engine;pthread_t thread;pthread_mutex_t mutex;
    int stop;unsigned revision;monitor_job tasks[TASKS],once[4];size_t count,once_count;
    char *results[RESULTS];size_t head,length;
};
typedef struct {
    int fd,family,received,waiting;struct sockaddr_storage address;socklen_t address_len;
    uint16_t id,sequence;uint8_t request[24];uint64_t sent;double min,max,sum;
    const char *error;
} echo_target;

static int clamp(int n,int low,int high,int fallback)
{return n<=0?fallback:n<low?low:n>high?high:n;}
static void timestamp(char out[32])
{time_t now=time(NULL);struct tm tm;gmtime_r(&now,&tm);strftime(out,32,"%Y-%m-%dT%H:%M:%SZ",&tm);}
static int canceled(vr_monitor *m,const monitor_job *j)
{pthread_mutex_lock(&m->mutex);int stop=m->stop||(!*j->request&&j->revision!=m->revision);pthread_mutex_unlock(&m->mutex);return stop;}
static void put_result(vr_monitor *m,json_object *o)
{
    const char *wire=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);size_t n=strlen(wire);if(n>16382)return;
    char *copy=malloc(n+2);if(!copy)return;memcpy(copy,wire,n);copy[n]='\n';copy[n+1]=0;
    pthread_mutex_lock(&m->mutex);
    if(m->length==RESULTS){free(m->results[m->head]);m->head=(m->head+1)%RESULTS;m->length--;}
    m->results[(m->head+m->length++)%RESULTS]=copy;pthread_mutex_unlock(&m->mutex);
}
static int parse_job(json_object *o,monitor_job *j,int task)
{
    memset(j,0,sizeof(*j));
    if(vr_copy(task?j->id:j->request,129,vr_string(o,task?"id":"request_id"))||!(task?*j->id:*j->request))return -1;
    j->count=clamp(json_object_get_int(vr_member(o,"count")),1,10,4);
    j->timeout=clamp(json_object_get_int(vr_member(o,"timeout_ms")),300,5000,1000);
    j->cycle=clamp(json_object_get_int(vr_member(o,"cycle_sec")),5,86400,60);
    json_object *targets=vr_member(o,"targets");
    if(!json_object_is_type(targets,json_type_array))return -1;
    for(size_t i=0;i<(size_t)json_object_array_length(targets);i++) {
        json_object *v=json_object_array_get_idx(targets,i);if(!json_object_is_type(v,json_type_string))return -1;
        const char *text=json_object_get_string(v);uint8_t ip[16];char canonical[46];
        int family=inet_pton(AF_INET,text,ip)==1?AF_INET:inet_pton(AF_INET6,text,ip)==1?AF_INET6:0;
        if(!family||!inet_ntop(family,ip,canonical,sizeof(canonical)))return -1;
        int duplicate=0;for(int k=0;k<j->length;k++)if(!strcmp(j->targets[k],canonical))duplicate=1;
        if(duplicate)continue;
        if(j->length>=TARGETS)return -1;
        strcpy(j->targets[j->length++],canonical);
    }
    return j->length?0:-1;
}

static void open_echo(echo_target *t,const char *ip,const char *wan)
{
    memset(t,0,sizeof(*t));t->fd=-1;struct sockaddr_in *v4=(struct sockaddr_in *)&t->address;
    struct sockaddr_in6 *v6=(struct sockaddr_in6 *)&t->address;
    if(inet_pton(AF_INET,ip,&v4->sin_addr)==1){t->family=AF_INET;v4->sin_family=AF_INET;t->address_len=sizeof(*v4);}
    else {t->family=AF_INET6;v6->sin6_family=AF_INET6;inet_pton(AF_INET6,ip,&v6->sin6_addr);t->address_len=sizeof(*v6);if(IN6_IS_ADDR_LINKLOCAL(&v6->sin6_addr))v6->sin6_scope_id=if_nametoindex(wan);}
    t->fd=socket(t->family,SOCK_RAW|SOCK_NONBLOCK|SOCK_CLOEXEC,t->family==AF_INET?IPPROTO_ICMP:IPPROTO_ICMPV6);
    if(t->fd<0){t->error="ICMP socket unavailable";return;}
    if(t->family==AF_INET6&&(!*wan||setsockopt(t->fd,SOL_SOCKET,SO_BINDTODEVICE,wan,strlen(wan)+1))){t->error="IPv6 WAN interface unavailable";close(t->fd);t->fd=-1;return;}
    if(connect(t->fd,(struct sockaddr *)&t->address,t->address_len)){t->error="ICMP target unreachable";close(t->fd);t->fd=-1;return;}
    char random[33];if(vr_random_hex(random,16)){t->error="ICMP random unavailable";close(t->fd);t->fd=-1;return;}
    /* Request identifier plus unpredictable echoed data prevents unrelated
       traffic (including other root ping processes) from counting as replies. */
    memcpy(t->request+8,random,16);t->id=(uint16_t)vr_checksum(random,32);
    t->request[4]=(uint8_t)(t->id>>8);t->request[5]=(uint8_t)t->id;
}
static int echo_reply(echo_target *t,const uint8_t *b,size_t n)
{
    if(t->family==AF_INET){if(n<20||b[0]>>4!=4)return 0;size_t ihl=(b[0]&15)*4u;if(ihl<20||ihl>n)return 0;b+=ihl;n-=ihl;}
    return n==sizeof(t->request)&&b[0]==(t->family==AF_INET?0:129)&&b[1]==0&&
        !memcmp(b+4,t->request+4,sizeof(t->request)-4)&&
        (t->family!=AF_INET||!vr_checksum(b,n));
}

static json_object *run_job(vr_monitor *m,const monitor_job *j)
{
    echo_target targets[TARGETS];struct pollfd fds[TARGETS];char started[32];timestamp(started);
    vr_addresses addresses=vr_engine_addresses(m->engine);
    for(int i=0;i<j->length;i++)open_echo(&targets[i],j->targets[i],addresses.wan_if);
    for(int round=0;round<j->count&&!canceled(m,j);round++) {
        int waiting=0;
        for(int i=0;i<j->length;i++) {
            echo_target *t=&targets[i];t->waiting=0;if(t->fd<0)continue;
            t->sequence=(uint16_t)(round+1);t->request[0]=t->family==AF_INET?8:128;t->request[2]=t->request[3]=0;
            t->request[6]=(uint8_t)(t->sequence>>8);t->request[7]=(uint8_t)t->sequence;
            if(t->family==AF_INET){uint16_t sum=vr_checksum(t->request,sizeof(t->request));t->request[2]=(uint8_t)(sum>>8);t->request[3]=(uint8_t)sum;}
            t->sent=vr_millis();
            if(send(t->fd,t->request,sizeof(t->request),0)==(ssize_t)sizeof(t->request)){t->waiting=1;waiting++;}else t->error="ICMP send failed";
        }
        uint64_t deadline=vr_millis()+(uint64_t)j->timeout;
        while(waiting&&!canceled(m,j)) {
            uint64_t now=vr_millis();if(now>=deadline)break;
            for(int i=0;i<j->length;i++)fds[i]=(struct pollfd){targets[i].waiting?targets[i].fd:-1,POLLIN,0};
            int delay=(int)(deadline-now);if(delay>100)delay=100;
            int rc=poll(fds,(nfds_t)j->length,delay);if(rc<0&&errno==EINTR)continue;if(rc<0)break;
            for(int i=0;i<j->length;i++)if(targets[i].waiting&&fds[i].revents) {
                echo_target *t=&targets[i];uint8_t bytes[2048];
                /* Bound draining under unsolicited ICMP traffic. */
                for(int k=0;k<16;k++){ssize_t n=recv(t->fd,bytes,sizeof(bytes),0);if(n<=0)break;if(!echo_reply(t,bytes,(size_t)n))continue;
                    double latency=(double)(vr_millis()-t->sent);if(!t->received||latency<t->min)t->min=latency;if(latency>t->max)t->max=latency;
                    t->sum+=latency;t->received++;t->waiting=0;waiting--;break;}
                if(fds[i].revents&(POLLERR|POLLHUP|POLLNVAL)){if(t->waiting){t->waiting=0;waiting--;}t->error="ICMP target unreachable";}
            }
        }
    }
    json_object *o=json_object_new_object(),*results=json_object_new_array();
    json_object_object_add(o,"type",json_object_new_string("network_monitor_result"));json_object_object_add(o,"node_id",json_object_new_string(m->engine->config.node_id));
    json_object_object_add(o,"ok",json_object_new_boolean(1));json_object_object_add(o,"count",json_object_new_int(j->count));json_object_object_add(o,"timeout_ms",json_object_new_int(j->timeout));
    if(*j->id){json_object_object_add(o,"task_id",json_object_new_string(j->id));json_object_object_add(o,"cycle_sec",json_object_new_int(j->cycle));}
    if(*j->request)json_object_object_add(o,"request_id",json_object_new_string(j->request));
    for(int i=0;i<j->length;i++) {
        echo_target *t=&targets[i];if(t->fd>=0)close(t->fd);json_object *r=json_object_new_object();
        json_object_object_add(r,"target",json_object_new_string(j->targets[i]));json_object_object_add(r,"ip_family",json_object_new_string(t->family==AF_INET?"ipv4":"ipv6"));
        json_object_object_add(r,"sent",json_object_new_int(j->count));json_object_object_add(r,"received",json_object_new_int(t->received));
        json_object_object_add(r,"loss_percent",json_object_new_double((double)(j->count-t->received)*100/j->count));
        if(t->received){json_object_object_add(r,"latency_min_ms",json_object_new_double(t->min));json_object_object_add(r,"latency_avg_ms",json_object_new_double(t->sum/t->received));json_object_object_add(r,"latency_max_ms",json_object_new_double(t->max));}
        else json_object_object_add(r,"error",json_object_new_string(t->error?t->error:"ICMP timeout"));
        json_object_array_add(results,r);
    }
    json_object_object_add(o,"results",results);json_object_object_add(o,"started_at",json_object_new_string(started));timestamp(started);
    json_object_object_add(o,"timestamp",json_object_new_string(started));json_object_object_add(o,"finished_at",json_object_new_string(started));return o;
}

static void *worker(void *arg)
{
    vr_monitor *m=arg;
    for(;;) {
        monitor_job job;int have=0;pthread_mutex_lock(&m->mutex);
        if(m->stop){pthread_mutex_unlock(&m->mutex);break;}
        if(m->once_count){job=m->once[0];m->once_count--;memmove(m->once,m->once+1,m->once_count*sizeof(job));have=1;}
        else {
            uint64_t now=vr_millis();size_t first=m->count;
            for(size_t i=0;i<m->count;i++)if(m->tasks[i].due<=now&&(first==m->count||m->tasks[i].due<m->tasks[first].due))first=i;
            if(first<m->count){job=m->tasks[first];m->tasks[first].due=now+(uint64_t)job.cycle*1000;have=1;}
        }
        pthread_mutex_unlock(&m->mutex);
        if(have){json_object *o=run_job(m,&job);if(!canceled(m,&job))put_result(m,o);json_object_put(o);}else poll(NULL,0,50);
    }
    return NULL;
}

int vr_monitor_start(vr_engine *e)
{
    vr_monitor *m=calloc(1,sizeof(*m));if(!m)return -1;m->engine=e;pthread_mutex_init(&m->mutex,NULL);
    pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,128*1024);
    int rc=pthread_create(&m->thread,&attr,worker,m);pthread_attr_destroy(&attr);
    if(rc){pthread_mutex_destroy(&m->mutex);free(m);return -1;}e->monitor=m;return 0;
}
void vr_monitor_stop(vr_engine *e)
{
    vr_monitor *m=e->monitor;if(!m)return;
    pthread_mutex_lock(&m->mutex);m->stop=1;pthread_mutex_unlock(&m->mutex);pthread_join(m->thread,NULL);
    for(size_t i=0;i<RESULTS;i++)free(m->results[i]);
    pthread_mutex_destroy(&m->mutex);free(m);e->monitor=NULL;
}
void vr_monitor_submit(vr_engine *e,json_object *message)
{
    vr_monitor *m=e->monitor;if(!m)return;
    if(!strcmp(vr_string(message,"type"),"network_monitor_tasks")) {
        json_object *tasks=vr_member(message,"tasks");if(tasks&&!json_object_is_type(tasks,json_type_array))return;
        /* Empty controller snapshots may omit tasks (Go omitempty). Stock
           json-c dereferences NULL here; treat omitted/null as an empty list. */
        size_t n=tasks?json_object_array_length(tasks):0;if(n>TASKS){vr_service_log(e->service,"ping","Too many network monitor tasks (maximum 32)");return;}
        monitor_job *next=calloc(TASKS,sizeof(*next));if(!next)return;size_t count=0;int valid=1;
        for(size_t i=0;i<n;i++) {json_object *task=json_object_array_get_idx(tasks,i);if(!json_object_get_boolean(vr_member(task,"enabled")))continue;
            if(parse_job(task,&next[count],1)){valid=0;break;}
            int duplicate=0;for(size_t j=0;j<count;j++)if(!strcmp(next[j].id,next[count].id))duplicate=1;if(!duplicate)count++;}
        if(valid){pthread_mutex_lock(&m->mutex);m->revision++;
            for(size_t i=0;i<count;i++){next[i].revision=m->revision;for(size_t k=0;k<m->count;k++)if(!strcmp(next[i].id,m->tasks[k].id)&&!memcmp(next[i].targets,m->tasks[k].targets,sizeof(next[i].targets))&&next[i].length==m->tasks[k].length&&next[i].count==m->tasks[k].count&&next[i].timeout==m->tasks[k].timeout&&next[i].cycle==m->tasks[k].cycle)next[i].due=m->tasks[k].due;}
            memcpy(m->tasks,next,count*sizeof(*next));m->count=count;pthread_mutex_unlock(&m->mutex);}
        else vr_service_log(e->service,"ping","Invalid network monitor tasks; retaining current tasks");
        free(next);
    } else {
        monitor_job job;const char *error=NULL;
        if(parse_job(message,&job,0))error="invalid network monitor request";
        else {pthread_mutex_lock(&m->mutex);if(m->once_count<4)m->once[m->once_count++]=job;else error="network monitor busy";pthread_mutex_unlock(&m->mutex);}
        if(error){json_object *o=json_object_new_object();json_object_object_add(o,"type",json_object_new_string("network_monitor_result"));json_object_object_add(o,"node_id",json_object_new_string(e->config.node_id));json_object_object_add(o,"request_id",json_object_new_string(vr_string(message,"request_id")));json_object_object_add(o,"ok",json_object_new_boolean(0));json_object_object_add(o,"error",json_object_new_string(error));char time[32];timestamp(time);json_object_object_add(o,"timestamp",json_object_new_string(time));put_result(m,o);json_object_put(o);}
    }
}
char *vr_monitor_take(vr_engine *e)
{
    vr_monitor *m=e->monitor;if(!m)return NULL;char *wire=NULL;pthread_mutex_lock(&m->mutex);
    if(m->length){wire=m->results[m->head];m->results[m->head]=NULL;m->head=(m->head+1)%RESULTS;m->length--;}
    pthread_mutex_unlock(&m->mutex);return wire;
}
