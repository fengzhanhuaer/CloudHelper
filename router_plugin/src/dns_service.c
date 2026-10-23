#define _GNU_SOURCE
#include "engine.h"
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

static int read_all(int fd,void *buffer,size_t n)
{
    size_t off=0;
    while(off<n){ssize_t k=recv(fd,(char *)buffer+off,n-off,0);if(k<0&&errno==EINTR)continue;if(k<=0)return -1;off+=(size_t)k;}return 0;
}

static int mapping_reply(const uint8_t *query,size_t qn,uint8_t *reply,size_t cap,size_t *rn,const vr_mapping *mapping)
{
    time_t remaining=mapping->expires-time(NULL);
    if(remaining<=0)return vr_dns_reply(query,qn,reply,cap,2,NULL,rn);
    return vr_dns_reply_ttl(query,qn,reply,cap,0,mapping->ip,(uint32_t)(remaining>600?600:remaining),rn);
}

/* Never put an already cached UDP answer behind blocking upstream lookups. */
static int cached_answer(vr_engine *e,const uint8_t *query,size_t qn,uint8_t *reply,size_t cap,size_t *rn)
{
    vr_question q;
    if(vr_dns_question(query,qn,&q)||(query[2]&0x80)||q.type!=1)return -1;
    pthread_mutex_lock(&e->mutex);int needs_ip=vr_policy_needs_ip(e->policy,q.name);
    vr_decision d=vr_policy_match(e->policy,q.name,NULL);d.revision=e->policy_revision;pthread_mutex_unlock(&e->mutex);
    if(needs_ip)return -1;
    vr_mapping m;
    if(d.action!=VR_PROXY||vr_mapping_lookup(e,q.name,NULL,d,&m))return -1;
    return mapping_reply(query,qn,reply,cap,rn,&m);
}

static vr_decision decision(vr_engine *e,const char *domain,const char *ip,int *needs_ip)
{
    pthread_mutex_lock(&e->mutex);
    vr_decision d=vr_policy_match(e->policy,domain,ip);d.revision=e->policy_revision;
    if(needs_ip)*needs_ip=vr_policy_needs_ip(e->policy,domain);
    pthread_mutex_unlock(&e->mutex);return d;
}

static int dns_answer(vr_engine *e,const uint8_t *query,size_t qn,uint8_t *reply,size_t cap,size_t *rn)
{
    vr_addresses addresses=vr_engine_addresses(e);
    vr_question q;if(vr_dns_question(query,qn,&q) || (query[2]&0x80))return -1;
    int needs_ip;vr_decision selected=decision(e,q.name,NULL,&needs_ip);
    int upstream_done=0;
    if((q.type==1||q.type==28||q.type==64||q.type==65)&&needs_ip) {
        char ip[16];int resolved;
        if(q.type==1) {
            if(vr_dns_chain(&e->config,addresses.wan_if,query,qn,reply,cap,rn))return vr_dns_reply(query,qn,reply,cap,2,NULL,rn);
            upstream_done=1;resolved=vr_dns_address4(reply,*rn,ip);
            if(resolved && ((reply[3]&15)!=0 || (reply[6]==0 && reply[7]==0)))return 0;
        } else resolved=vr_resolve_config4(q.name,&e->config,addresses.wan_if,ip);
        if(resolved)return vr_dns_reply(query,qn,reply,cap,2,NULL,rn);
        selected=decision(e,q.name,ip,NULL);
    }
    if(selected.action==VR_REJECT)return vr_dns_reply(query,qn,reply,cap,5,NULL,rn);
    if(selected.action==VR_PROXY) {
        if(q.type==28||q.type==64||q.type==65)return vr_dns_reply(query,qn,reply,cap,0,NULL,rn);
        if(q.type==1) {
            vr_mapping mapping;
            if(vr_mapping_get(e,q.name,NULL,selected,&mapping))return vr_dns_reply(query,qn,reply,cap,2,NULL,rn);
            return mapping_reply(query,qn,reply,cap,rn,&mapping);
        }
    }
    if(!upstream_done && vr_dns_chain(&e->config,addresses.wan_if,query,qn,reply,cap,rn))return vr_dns_reply(query,qn,reply,cap,2,NULL,rn);
    return 0;
}

static void *dns_worker(void *arg)
{
    vr_engine *e=arg;
    while(1) {
        pthread_mutex_lock(&e->mutex);
        while(!e->dns_stop&&!e->job_count)pthread_cond_wait(&e->jobs_changed,&e->mutex);
        if(e->dns_stop){pthread_mutex_unlock(&e->mutex);break;}
        vr_dns_job job=e->jobs[e->job_head];e->job_head=(e->job_head+1)%16;e->job_count--;pthread_mutex_unlock(&e->mutex);
        uint8_t reply[4096];size_t n=0;int valid=1;
        if(job.tcp>=0) {
            struct timeval timeout={3,0};setsockopt(job.tcp,SOL_SOCKET,SO_RCVTIMEO,&timeout,sizeof(timeout));setsockopt(job.tcp,SOL_SOCKET,SO_SNDTIMEO,&timeout,sizeof(timeout));
            uint8_t h[2];if(read_all(job.tcp,h,2))valid=0;
            if(valid){job.n=(size_t)((h[0]<<8)|h[1]);if(job.n>sizeof(job.query)||read_all(job.tcp,job.query,job.n))valid=0;}
        }
        if(valid&&!dns_answer(e,job.query,job.n,reply,sizeof(reply),&n)) {
            if(job.tcp<0)sendto(e->dns_udp,reply,n,MSG_NOSIGNAL,(struct sockaddr *)&job.source,job.source_len);
            else {
                uint8_t wire[4098];wire[0]=(uint8_t)(n>>8);wire[1]=(uint8_t)n;memcpy(wire+2,reply,n);
                size_t off=0;
                while(off<n+2){ssize_t k=send(job.tcp,wire+off,n+2-off,MSG_NOSIGNAL);if(k<0&&errno==EINTR)continue;if(k<=0)break;off+=(size_t)k;}
            }
        }
        if(job.tcp>=0)close(job.tcp);
    }
    return NULL;
}

int vr_dns_start(vr_engine *e)
{
    e->dns_udp=vr_bind4("127.0.0.1",1053,0);e->dns_tcp=vr_bind4("127.0.0.1",1053,1);
    if(e->dns_udp<0||e->dns_tcp<0)return -1;
    pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,128*1024);
    for(size_t i=0;i<4;i++) {
        if(pthread_create(&e->dns_workers[i],&attr,dns_worker,e)){pthread_attr_destroy(&attr);return -1;}
        e->dns_worker_count++;
    }
    pthread_attr_destroy(&attr);return vr_platform_dns(1);
}

void vr_dns_accept(vr_engine *e,int tcp)
{
    vr_dns_job job={0};job.tcp=-1;job.source_len=sizeof(job.source);
    if(tcp){job.tcp=accept4(e->dns_tcp,NULL,NULL,SOCK_CLOEXEC);if(job.tcp<0)return;}
    else {
        ssize_t n=recvfrom(e->dns_udp,job.query,sizeof(job.query),MSG_TRUNC,(struct sockaddr *)&job.source,&job.source_len);if(n<=0||(size_t)n>sizeof(job.query))return;job.n=(size_t)n;
        uint8_t reply[4096];size_t rn;
        if(!cached_answer(e,job.query,job.n,reply,sizeof(reply),&rn)) {
            sendto(e->dns_udp,reply,rn,MSG_NOSIGNAL,(struct sockaddr *)&job.source,job.source_len);return;
        }
    }
    pthread_mutex_lock(&e->mutex);
    if(e->job_count==16 || e->dns_stop){pthread_mutex_unlock(&e->mutex);if(job.tcp>=0)close(job.tcp);return;}
    e->jobs[(e->job_head+e->job_count)%16]=job;e->job_count++;pthread_cond_signal(&e->jobs_changed);pthread_mutex_unlock(&e->mutex);
}

void vr_dns_stop(vr_engine *e)
{
    pthread_mutex_lock(&e->mutex);e->dns_stop=1;pthread_cond_broadcast(&e->jobs_changed);pthread_mutex_unlock(&e->mutex);
    for(size_t i=0;i<e->dns_worker_count;i++)pthread_join(e->dns_workers[i],NULL);
    for(size_t i=0;i<e->job_count;i++){vr_dns_job *j=&e->jobs[(e->job_head+i)%16];if(j->tcp>=0)close(j->tcp);}
    if(e->dns_udp>=0)close(e->dns_udp);
    if(e->dns_tcp>=0)close(e->dns_tcp);
    e->dns_udp=e->dns_tcp=-1;
}
