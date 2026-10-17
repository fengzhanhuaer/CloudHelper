#define _GNU_SOURCE
#include "chvr.h"
#include <arpa/inet.h>
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>
#include <openssl/rand.h>

static uint16_t u16(const uint8_t *p){return (uint16_t)((p[0]<<8)|p[1]);}
static void p16(uint8_t *p,uint16_t v){p[0]=(uint8_t)(v>>8);p[1]=(uint8_t)v;}

static int dns_name(const uint8_t *b,size_t n,size_t *offset,char out[254])
{
    size_t pos=*offset,used=0,end=0;unsigned steps=0;
    while(pos<n && ++steps<128) {
        unsigned len=b[pos++];
        if(!len){if(!end)end=pos;if(!used)out[used++]='.';out[used]=0;*offset=end;return 0;}
        if((len&0xc0)==0xc0) {
            if(pos>=n)return -1;
            size_t target=((len&63)<<8)|b[pos++];
            if(target>=pos-2)return -1; /* Only backward compression references. */
            if(!end)end=pos;
            pos=target;continue;
        }
        if(len>63 || pos+len>n || used+len+(used?1:0)>253)return -1;
        if(used)out[used++]='.';
        for(unsigned i=0;i<len;i++) {
            unsigned char c=b[pos++];
            if(c<=32 || c>=127 || c=='.' || c=='\\')return -1;
            out[used++]=(char)(c>='A'&&c<='Z'?c+32:c);
        }
    }
    return -1;
}

int vr_dns_question(const uint8_t *b,size_t n,vr_question *q)
{
    if(n<12 || u16(b+4)!=1 || (b[2]&0x78))return -1;
    size_t off=12;
    if(dns_name(b,n,&off,q->name) || off+4>n || u16(b+off+2)!=1)return -1;
    q->id=u16(b);q->type=u16(b+off);q->end=off+4;return 0;
}

static int put_question(uint8_t *b,size_t cap,const char *host,uint16_t type,size_t *n)
{
    size_t off=12;const char *p=!strcmp(host,".")?"":host;
    while(*p) {
        const char *dot=strchr(p,'.');size_t k=dot?(size_t)(dot-p):strlen(p);
        if(!k || k>63 || off+k+6>cap)return -1;
        b[off++]=(uint8_t)k;memcpy(b+off,p,k);off+=k;
        if(!dot)break;
        p=dot+1;
    }
    if(off+5>cap)return -1;
    b[off++]=0;p16(b+off,type);p16(b+off+2,1);*n=off+4;return 0;
}

int vr_dns_reply_ttl(const uint8_t *query,size_t qn,uint8_t *out,size_t cap,int rcode,const char *ip,uint32_t ttl,size_t *n)
{
    vr_question q;struct in_addr addr;
    if(vr_dns_question(query,qn,&q) || cap<12 || (query[2]&0x80) || rcode<0 || rcode>15)return -1;
    if(ip && (q.type!=1 || inet_pton(AF_INET,ip,&addr)!=1))return -1;
    memset(out,0,12);p16(out,q.id);out[2]=(uint8_t)(0x80|(query[2]&1));out[3]=(uint8_t)(0x80|rcode);p16(out+4,1);
    if(put_question(out,cap,q.name,q.type,n))return -1;
    if(!ip || rcode)return 0;
    if(*n+16>cap)return -1;
    uint8_t *p=out+*n;
    p16(p,0xc00c);p16(p+2,1);p16(p+4,1);p16(p+6,(uint16_t)(ttl>>16));p16(p+8,(uint16_t)ttl);p16(p+10,4);memcpy(p+12,&addr,4);
    *n+=16;p16(out+6,1);return 0;
}

int vr_dns_reply(const uint8_t *query,size_t qn,uint8_t *out,size_t cap,int rcode,const char *ip,size_t *n)
{return vr_dns_reply_ttl(query,qn,out,cap,rcode,ip,30,n);}

static int transfer(int fd,void *buf,size_t n,int sending)
{
    size_t off=0;
    while(off<n) {
        ssize_t k=sending?send(fd,(char *)buf+off,n-off,MSG_NOSIGNAL):recv(fd,(char *)buf+off,n-off,0);
        if(k<0 && errno==EINTR)continue;
        if(k<=0)return -1;
        off+=(size_t)k;
    }
    return 0;
}

int vr_dns_exchange(const char *upstream,const char *iface,const void *query,size_t qn,void *reply,size_t cap,size_t *n,int tcp)
{
    char host[64];if(vr_copy(host,sizeof(host),upstream))return -1;
    char *sep=strrchr(host,':');if(!sep)return -1;char *end;long port=strtol(sep+1,&end,10);if(*end || port<1 || port>65535)return -1;*sep=0;
    struct sockaddr_in sa={0};sa.sin_family=AF_INET;sa.sin_port=htons((uint16_t)port);
    vr_question sent,received;
    if(qn>4096 || inet_pton(AF_INET,host,&sa.sin_addr)!=1 || vr_dns_question(query,qn,&sent))return -1;
    int fd=socket(AF_INET,(tcp?SOCK_STREAM:SOCK_DGRAM)|SOCK_CLOEXEC,0);if(fd<0)return -1;
    struct timeval timeout={3,0};setsockopt(fd,SOL_SOCKET,SO_RCVTIMEO,&timeout,sizeof(timeout));setsockopt(fd,SOL_SOCKET,SO_SNDTIMEO,&timeout,sizeof(timeout));
    int rc=-1;
    if(iface && *iface && setsockopt(fd,SOL_SOCKET,SO_BINDTODEVICE,iface,strlen(iface)+1))goto done;
    if(connect(fd,(struct sockaddr *)&sa,sizeof(sa)))goto done;
    if(tcp) {
        uint8_t h[2];p16(h,(uint16_t)qn);
        if(transfer(fd,h,2,1) || transfer(fd,(void *)query,qn,1) || transfer(fd,h,2,0))goto done;
        *n=u16(h);if(*n>cap || transfer(fd,reply,*n,0))goto done;
    } else {
        if(send(fd,query,qn,0)!=(ssize_t)qn)goto done;
        ssize_t k=recv(fd,reply,cap,MSG_TRUNC);if(k<0 || (size_t)k>cap)goto done;*n=(size_t)k;
    }
    if(vr_dns_question(reply,*n,&received) || !(((uint8_t *)reply)[2]&0x80) || received.id!=sent.id || received.type!=sent.type || strcmp(received.name,sent.name) || (((uint8_t *)reply)[3]&15)==2 || (((uint8_t *)reply)[3]&15)==5)goto done;
    rc=0;
done:close(fd);
    if(!rc && !tcp && (((uint8_t *)reply)[2]&2))return vr_dns_exchange(upstream,iface,query,qn,reply,cap,n,1);
    return rc;
}

int vr_dns_addresses4(const uint8_t *reply,size_t n,char out[4][16])
{
    struct in_addr ip;
    vr_question q;if(vr_dns_question(reply,n,&q))return -1;
    if(!(reply[2]&0x80)||(reply[3]&15))return -1;
    size_t off=q.end;unsigned count=u16(reply+6);int used=0;
    for(unsigned i=0;i<count && i<128;i++) {
        char name[254];if(dns_name(reply,n,&off,name) || off+10>n)return -1;
        uint16_t type=u16(reply+off),cls=u16(reply+off+2),len=u16(reply+off+8);off+=10;
        if(off+len>n)return -1;
        if(type==1 && cls==1 && len==4 && used<4) {
            char text[16];memcpy(&ip,reply+off,4);if(!inet_ntop(AF_INET,&ip,text,sizeof(text)))return -1;
            int duplicate=0;for(int j=0;j<used;j++)if(!strcmp(out[j],text))duplicate=1;
            if(!duplicate)strcpy(out[used++],text);
        }
        off+=len;
    }
    return used?used:-1;
}

int vr_dns_address4(const uint8_t *reply,size_t n,char out[16])
{char addresses[4][16];if(vr_dns_addresses4(reply,n,addresses)<1)return -1;return vr_copy(out,16,addresses[0]);}

int vr_resolve4(const char *host,const char *upstream,const char *iface,char out[16])
{
    struct in_addr ip;
    if(inet_pton(AF_INET,host,&ip)==1)return vr_copy(out,16,host);
    uint8_t query[512]={0},reply[4096];size_t qn,n;
    if(RAND_bytes(query,2)!=1)return -1;
    query[2]=1;query[5]=1;
    if(put_question(query,sizeof(query),host,1,&qn)||vr_dns_exchange(upstream,iface,query,qn,reply,sizeof(reply),&n,0))return -1;
    return vr_dns_address4(reply,n,out);
}
