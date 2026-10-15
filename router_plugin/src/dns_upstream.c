#define _GNU_SOURCE
#include "transport.h"
#include <arpa/inet.h>
#include <curl/curl.h>
#include <openssl/rand.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <ifaddrs.h>

/* Keep this wire contract aligned with Probe/Android dnsupstream.Config. */
int vr_dns_endpoint(const char *text,char host[254],int *port,int literal)
{
    const char *sep=strrchr(text,':');if(!sep || sep==text || sep-text>253)return -1;
    for(const char *p=sep+1;*p;p++)if(*p<'0'||*p>'9')return -1;
    if(!sep[1])return -1;
    long n=strtol(sep+1,NULL,10);if(n<1||n>65535)return -1;
    memcpy(host,text,(size_t)(sep-text));host[sep-text]=0;*port=(int)n;
    struct in_addr ip;
    if(inet_pton(AF_INET,host,&ip)==1) {
        uint32_t v=ntohl(ip.s_addr);
        return (!v || (v>>24)==127 || v>=0xe0000000u || (v&0xfffe0000u)==0xc6120000u)?-1:0;
    }
    if(literal)return -1;
    for(const char *p=host;*p;p++)if(!((*p>='a'&&*p<='z')||(*p>='A'&&*p<='Z')||(*p>='0'&&*p<='9')||*p=='.'||*p=='-'))return -1;
    return 0;
}

int vr_dns_doh_endpoint(const char *text,char host[254],int *port)
{
    if(strncmp(text,"https://",8) || strlen(text)>1023)return -1;
    for(const unsigned char *p=(const unsigned char *)text;*p;p++)if(*p<=32||*p>=127||*p=='#'||*p=='\\')return -1;
    const char *p=text+8;size_t n=strcspn(p,"/?");char authority[260];
    if(!n||n>=sizeof(authority))return -1;
    memcpy(authority,p,n);authority[n]=0;
    if(!strchr(authority,':'))strcat(authority,":443");
    return vr_dns_endpoint(authority,host,port,0);
}

static int local_address(const char *ip)
{
    struct ifaddrs *interfaces=NULL;if(getifaddrs(&interfaces))return 1;
    int local=0;char text[16];
    for(struct ifaddrs *p=interfaces;p;p=p->ifa_next)if(p->ifa_addr&&p->ifa_addr->sa_family==AF_INET&&
        inet_ntop(AF_INET,&((struct sockaddr_in *)p->ifa_addr)->sin_addr,text,sizeof(text))&&!strcmp(text,ip))local=1;
    freeifaddrs(interfaces);return local;
}

static int system_servers(char out[4][64])
{
    /* Prefer the current vendor file. When intercepted, use its RAM backup. */
    const char *paths[]={"/tmp/resolv.dnsmasq","/tmp/cloudhelper-dns.original","/etc/resolv.conf"};
    int count=0;
    for(unsigned i=0;i<3 && !count;i++) {
        char buf[16384];size_t n;if(vr_read_file(paths[i],buf,sizeof(buf)-1,&n))continue;buf[n]=0;
        char *state=NULL;
        for(char *line=strtok_r(buf,"\n",&state);line&&count<4;line=strtok_r(NULL,"\n",&state)) {
            char ip[64];if(sscanf(line,"server=%63s",ip)!=1 && sscanf(line,"nameserver %63s",ip)!=1)continue;
            char *hash=strchr(ip,'#');if(hash)*hash=0;
            struct in_addr addr;if(inet_pton(AF_INET,ip,&addr)!=1||local_address(ip))continue;
            char address[64],host[254];int port;snprintf(address,sizeof(address),"%.15s:53",ip);
            if(vr_dns_endpoint(address,host,&port,1))continue;
            int duplicate=0;for(int j=0;j<count;j++)if(!strcmp(out[j],address))duplicate=1;
            if(!duplicate)strcpy(out[count++],address);
        }
    }
    return count;
}

static int bootstrap(const char *host,const vr_config *c,const char *iface,char ip[16])
{
    struct in_addr addr;if(inet_pton(AF_INET,host,&addr)==1)return vr_copy(ip,16,host);
    if(!strcmp(host,"dns.alidns.com"))return vr_copy(ip,16,"223.5.5.5");
    if(!vr_resolve4(host,c->dns_upstream,iface,ip))return 0;
    char servers[4][64];int n=system_servers(servers);
    for(int i=0;i<n;i++)if(strcmp(servers[i],c->dns_upstream)&&!vr_resolve4(host,servers[i],iface,ip))return 0;
    return -1;
}

typedef struct {uint8_t *bytes;size_t used,cap;} dns_buffer;
static size_t receive(void *bytes,size_t size,size_t count,void *arg)
{
    dns_buffer *b=arg;if(size && count>(b->cap-b->used)/size)return 0;
    size_t n=size*count;memcpy(b->bytes+b->used,bytes,n);b->used+=n;return n;
}

static int valid(const void *query,size_t qn,const void *reply,size_t rn)
{
    vr_question q,a;const uint8_t *b=reply;
    return vr_dns_question(query,qn,&q)||vr_dns_question(reply,rn,&a)||!(b[2]&0x80)||(b[2]&2)||
        (b[3]&15)==2||(b[3]&15)==5||q.id!=a.id||q.type!=a.type||strcmp(q.name,a.name)?-1:0;
}

static int doh(const vr_config *c,const char *iface,const void *query,size_t qn,void *reply,size_t cap,size_t *n)
{
    char host[254],ip[16],address[320];int port;
    if(vr_dns_doh_endpoint(c->dns_doh,host,&port)||bootstrap(host,c,iface,ip))return -1;
    snprintf(address,sizeof(address),"%s:%d:%s",host,port,ip);
    struct curl_slist *resolve=curl_slist_append(NULL,address),*headers=NULL;
    headers=curl_slist_append(headers,"Content-Type: application/dns-message");headers=curl_slist_append(headers,"Accept: application/dns-message");
    CURL *curl=curl_easy_init();int rc=-1;dns_buffer b={reply,0,cap};
    if(!curl||!resolve||!headers)goto done;
    curl_easy_setopt(curl,CURLOPT_URL,c->dns_doh);curl_easy_setopt(curl,CURLOPT_PROXY,"");
    curl_easy_setopt(curl,CURLOPT_PROTOCOLS,(long)CURLPROTO_HTTPS);curl_easy_setopt(curl,CURLOPT_FOLLOWLOCATION,0L);
    curl_easy_setopt(curl,CURLOPT_CONNECTTIMEOUT,3L);curl_easy_setopt(curl,CURLOPT_TIMEOUT,3L);curl_easy_setopt(curl,CURLOPT_NOSIGNAL,1L);
    curl_easy_setopt(curl,CURLOPT_SSL_VERIFYPEER,1L);curl_easy_setopt(curl,CURLOPT_SSL_VERIFYHOST,2L);
    curl_easy_setopt(curl,CURLOPT_SSLVERSION,(long)CURL_SSLVERSION_TLSv1_2);
    const char *ca=getenv("SSL_CERT_FILE");if(ca&&*ca)curl_easy_setopt(curl,CURLOPT_CAINFO,ca);
    curl_easy_setopt(curl,CURLOPT_RESOLVE,resolve);curl_easy_setopt(curl,CURLOPT_HTTPHEADER,headers);
    curl_easy_setopt(curl,CURLOPT_POSTFIELDS,query);curl_easy_setopt(curl,CURLOPT_POSTFIELDSIZE,(long)qn);
    curl_easy_setopt(curl,CURLOPT_WRITEFUNCTION,receive);curl_easy_setopt(curl,CURLOPT_WRITEDATA,&b);
    if(iface&&*iface)curl_easy_setopt(curl,CURLOPT_INTERFACE,iface);
    CURLcode result=curl_easy_perform(curl);long status=0;char *type=NULL;
    curl_easy_getinfo(curl,CURLINFO_RESPONSE_CODE,&status);curl_easy_getinfo(curl,CURLINFO_CONTENT_TYPE,&type);
    if(result==CURLE_OK&&status==200&&type&&!strncasecmp(type,"application/dns-message",23)&&
       (!type[23]||type[23]==';')&&!valid(query,qn,reply,b.used)){*n=b.used;rc=0;}
done:
    if(curl)curl_easy_cleanup(curl);
    curl_slist_free_all(resolve);curl_slist_free_all(headers);return rc;
}

static int dot(const vr_config *c,const char *iface,const void *query,size_t qn,void *reply,size_t cap,size_t *n)
{
    char host[254],ip[16];int port,fd;
    if(vr_dns_endpoint(c->dns_dot,host,&port,0)||bootstrap(host,c,iface,ip))return -1;
    uint64_t deadline=vr_millis()+3000;SSL *ssl=vr_tls_connect_at(host,ip,port,"",iface,&fd,deadline);if(!ssl)return -1;
    uint8_t h[2]={(uint8_t)(qn>>8),(uint8_t)qn};int rc=-1;
    if(vr_tls_io(ssl,1,h,2,deadline)||vr_tls_io(ssl,1,(void *)query,qn,deadline)||vr_tls_io(ssl,0,h,2,deadline))goto done;
    *n=(h[0]<<8)|h[1];if(*n>cap||*n<12||vr_tls_io(ssl,0,reply,*n,deadline)||valid(query,qn,reply,*n))goto done;
    rc=0;
done:SSL_free(ssl);close(fd);return rc;
}

int vr_dns_chain(const vr_config *c,const char *iface,const void *query,size_t qn,void *reply,size_t cap,size_t *n)
{
    if(qn<12||qn>4096)return -1;
    if(*c->dns_doh&&!doh(c,iface,query,qn,reply,cap,n))return 0;
    if(*c->dns_dot&&!dot(c,iface,query,qn,reply,cap,n))return 0;
    if(!vr_dns_exchange(c->dns_upstream,iface,query,qn,reply,cap,n,0))return 0;
    char servers[4][64];int count=system_servers(servers);
    for(int i=0;i<count;i++)if(strcmp(servers[i],c->dns_upstream)&&!vr_dns_exchange(servers[i],iface,query,qn,reply,cap,n,0))return 0;
    return -1;
}

int vr_resolve_config4(const char *host,const vr_config *c,const char *iface,char out[16])
{
    struct in_addr ip;if(inet_pton(AF_INET,host,&ip)==1)return vr_copy(out,16,host);
    uint8_t query[512]={0},reply[4096];size_t off=12,n;
    if(RAND_bytes(query,2)!=1)return -1;
    query[2]=1;query[5]=1;const char *p=host;
    while(*p){const char *dot=strchr(p,'.');size_t k=dot?(size_t)(dot-p):strlen(p);if(!k||k>63||off+k+6>sizeof(query))return -1;
        query[off++]=(uint8_t)k;memcpy(query+off,p,k);off+=k;if(!dot)break;p=dot+1;}
    query[off++]=0;query[off++]=0;query[off++]=1;query[off++]=0;query[off++]=1;
    if(vr_dns_chain(c,iface,query,off,reply,sizeof(reply),&n))return -1;
    return vr_dns_address4(reply,n,out);
}
