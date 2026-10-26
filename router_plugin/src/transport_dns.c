#define _GNU_SOURCE
#include "transport.h"
#include <arpa/inet.h>
#include <errno.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <openssl/rand.h>

int vr_hostport(const char *host,int port,char *out,size_t cap)
{
    int n=snprintf(out,cap,strchr(host,':')?"[%s]:%d":"%s:%d",host,port);
    return n<0||(size_t)n>=cap?-1:0;
}
int vr_curl_address(const char *host,int port,const char *ip,char *out,size_t cap)
{
    /* Numeric URL hosts need no DNS override (and old curl cannot parse an
       IPv6 literal in the HOST part of CURLOPT_RESOLVE). */
    unsigned char literal[16];if(inet_pton(AF_INET6,host,literal)==1){*out=0;return 0;}
    int n=snprintf(out,cap,strchr(ip,':')?"%s:%d:[%s]":"%s:%d:%s",host,port,ip);
    return n<0||(size_t)n>=cap?-1:0;
}

typedef struct {
    pthread_mutex_t mutex;pthread_cond_t changed;
    vr_config config;char host[254],iface[64],v4[4][16],v6[4][46];
    int refs,done,count4,count6;
} lookup;
static pthread_mutex_t budget_mutex=PTHREAD_MUTEX_INITIALIZER;
static int active;
static void release(lookup *j)
{
    int last=!--j->refs;pthread_mutex_unlock(&j->mutex);
    if(last){pthread_mutex_destroy(&j->mutex);pthread_cond_destroy(&j->changed);OPENSSL_cleanse(&j->config,sizeof(j->config));free(j);
        pthread_mutex_lock(&budget_mutex);active--;pthread_mutex_unlock(&budget_mutex);}
}
static void *family(void *arg)
{
    lookup *j=arg;char v4[4][16];int n4=vr_resolve_config4_all(j->host,&j->config,j->iface,v4);
    pthread_mutex_lock(&j->mutex);j->count4=n4;if(n4>0)memcpy(j->v4,v4,sizeof(v4));j->done++;pthread_cond_signal(&j->changed);release(j);return NULL;
}
static void *family6(void *arg)
{
    lookup *j=arg;uint8_t query[512]={0},reply[4096];size_t off=12,n;int count=-1;
    if(RAND_bytes(query,2)!=1)goto done;
    query[2]=1;query[5]=1;const char *p=j->host;
    while(*p){const char *dot=strchr(p,'.');size_t k=dot?(size_t)(dot-p):strlen(p);if(!k||k>63||off+k+6>sizeof(query))goto done;
        query[off++]=(uint8_t)k;memcpy(query+off,p,k);off+=k;if(!dot)break;p=dot+1;}
    query[off++]=0;query[off++]=0;query[off++]=28;query[off++]=0;query[off++]=1;
    char ips[4][46];
    if(!vr_dns_chain(&j->config,j->iface,query,off,reply,sizeof(reply),&n))count=vr_dns_addresses6(reply,n,ips);
done:
    pthread_mutex_lock(&j->mutex);j->count6=count;if(count>0)memcpy(j->v6,ips,sizeof(ips));j->done++;pthread_cond_signal(&j->changed);release(j);return NULL;
}
static struct timespec after_ms(unsigned ms)
{struct timespec t;clock_gettime(CLOCK_REALTIME,&t);t.tv_sec+=ms/1000;t.tv_nsec+=(long)(ms%1000)*1000000;if(t.tv_nsec>=1000000000){t.tv_sec++;t.tv_nsec-=1000000000;}return t;}

int vr_resolve_transport(const char *host,const vr_config *config,const char *iface,char out[4][46])
{
    unsigned char ip[16];
    if(inet_pton(AF_INET,host,ip)==1||inet_pton(AF_INET6,host,ip)==1){if(vr_copy(out[0],46,host))return -1;return 1;}
    pthread_mutex_lock(&budget_mutex);if(active>=8){pthread_mutex_unlock(&budget_mutex);return -1;}active++;pthread_mutex_unlock(&budget_mutex);
    lookup *j=calloc(1,sizeof(*j));if(!j){pthread_mutex_lock(&budget_mutex);active--;pthread_mutex_unlock(&budget_mutex);return -1;}
    pthread_mutex_init(&j->mutex,NULL);pthread_cond_init(&j->changed,NULL);j->refs=1;j->config=*config;
    snprintf(j->host,sizeof(j->host),"%s",host);snprintf(j->iface,sizeof(j->iface),"%s",iface?iface:"");
    pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,256*1024);pthread_attr_setdetachstate(&attr,PTHREAD_CREATE_DETACHED);
    pthread_mutex_lock(&j->mutex);
    for(int i=0;i<2;i++){pthread_t thread;j->refs++;if(pthread_create(&thread,&attr,i?family6:family,j)){j->refs--;j->done++;}}
    pthread_attr_destroy(&attr);struct timespec end=after_ms(5000);int grace=0;
    while(j->done<2){
        if(!grace&&(j->count4>0||j->count6>0)){end=after_ms(200);grace=1;}
        if(pthread_cond_timedwait(&j->changed,&j->mutex,&end)==ETIMEDOUT)break;
    }
    int count=0;
    for(int i=0;i<4&&count<4;i++){
        if(i<j->count4)strcpy(out[count++],j->v4[i]);
        if(i<j->count6&&count<4)strcpy(out[count++],j->v6[i]);
    }
    release(j);return count?count:-1;
}
