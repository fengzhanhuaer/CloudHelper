#define _GNU_SOURCE
#include "transport.h"
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <netinet/tcp.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/socket.h>
#include <unistd.h>
#include <openssl/pem.h>
#include <openssl/err.h>
#include <openssl/sha.h>
#include <openssl/x509v3.h>

uint64_t vr_millis(void){struct timespec t;clock_gettime(CLOCK_MONOTONIC,&t);return (uint64_t)t.tv_sec*1000+(uint64_t)t.tv_nsec/1000000;}

void vr_tls_error(char *out,size_t cap,const char *operation,int error)
{
    unsigned long code=ERR_peek_last_error();
    const char *reason=ERR_reason_error_string(code);
    char unknown[48];
    if(!reason && code){snprintf(unknown,sizeof(unknown),"OpenSSL error %08lx",code);reason=unknown;}
    if(!reason&&error==SSL_ERROR_SYSCALL&&errno)reason=strerror(errno);
    snprintf(out,cap,"TLS %s failed (%d): %.64s",operation,error,reason?reason:"connection closed");
}

static int wait_fd(int fd,short events,uint64_t deadline)
{
    uint64_t now=vr_millis();if(now>=deadline)return -1;
    struct pollfd p={fd,events,0};int n;
    do{n=poll(&p,1,(int)(deadline-now));}while(n<0&&errno==EINTR && vr_millis()<deadline);
    return n>0 && (p.revents&events)?0:-1;
}

int vr_tls_io(SSL *ssl,int write,void *bytes,size_t n,uint64_t deadline)
{
    size_t off=0;
    while(off<n) {
        ERR_clear_error();errno=0;
        int k=write?SSL_write(ssl,(char *)bytes+off,(int)(n-off)):SSL_read(ssl,(char *)bytes+off,(int)(n-off));
        if(k>0){off+=(size_t)k;continue;}
        int err=SSL_get_error(ssl,k);short events=err==SSL_ERROR_WANT_READ?POLLIN:err==SSL_ERROR_WANT_WRITE?POLLOUT:0;
        if(!events || wait_fd(SSL_get_fd(ssl),events,deadline))return -1;
    }
    return 0;
}

static int handshake(SSL *ssl,int client,uint64_t deadline)
{
    while(vr_millis()<deadline) {
        ERR_clear_error();errno=0;
        int n=client?SSL_connect(ssl):SSL_accept(ssl);if(n==1)return 0;
        int err=SSL_get_error(ssl,n);short events=err==SSL_ERROR_WANT_READ?POLLIN:err==SSL_ERROR_WANT_WRITE?POLLOUT:0;
        if(!events || wait_fd(SSL_get_fd(ssl),events,deadline))return -1;
    }
    return -1;
}

SSL_CTX *vr_tls_server_context(const char *cert,const char *key)
{
    SSL_CTX *ctx=SSL_CTX_new(TLS_server_method());if(!ctx)return NULL;
    SSL_CTX_set_min_proto_version(ctx,TLS1_2_VERSION);SSL_CTX_set_options(ctx,SSL_OP_NO_COMPRESSION);
    BIO *b=BIO_new_mem_buf(cert,-1),*k=BIO_new_mem_buf(key,-1);
    X509 *x=b?PEM_read_bio_X509(b,NULL,NULL,NULL):NULL;EVP_PKEY *p=k?PEM_read_bio_PrivateKey(k,NULL,NULL,NULL):NULL;
    int ok=x&&p&&SSL_CTX_use_certificate(ctx,x)==1&&SSL_CTX_use_PrivateKey(ctx,p)==1&&SSL_CTX_check_private_key(ctx)==1;
    X509_free(x);EVP_PKEY_free(p);
    if(ok)while((x=PEM_read_bio_X509(b,NULL,NULL,NULL))!=NULL) {if(SSL_CTX_add_extra_chain_cert(ctx,x)!=1){X509_free(x);ok=0;break;}}
    BIO_free(b);BIO_free(k);if(!ok){SSL_CTX_free(ctx);return NULL;}return ctx;
}

SSL *vr_tls_accept(SSL_CTX *ctx,int fd,uint64_t deadline)
{
    if(fcntl(fd,F_SETFL,O_NONBLOCK))return NULL;
    int yes=1;if(setsockopt(fd,IPPROTO_TCP,TCP_NODELAY,&yes,sizeof(yes)))return NULL;
    SSL *ssl=SSL_new(ctx);if(!ssl)return NULL;
    SSL_set_fd(ssl,fd);
    if(handshake(ssl,0,deadline)){SSL_free(ssl);return NULL;}return ssl;
}

int vr_tls_pinned(X509 *cert,const char *pin)
{
    if(!cert || strlen(pin)!=64 || X509_cmp_current_time(X509_get0_notBefore(cert))>=0 || X509_cmp_current_time(X509_get0_notAfter(cert))<=0)return -1;
    unsigned char *der=NULL,digest[32];int len=i2d_X509_PUBKEY(X509_get_X509_PUBKEY(cert),&der);
    if(len<=0)return -1;
    SHA256(der,(size_t)len,digest);OPENSSL_free(der);char text[65];
    for(unsigned i=0;i<32;i++)snprintf(text+2*i,3,"%02x",digest[i]);
    return strcasecmp(text,pin)?-1:0;
}

static SSL *connect_at(const char *host,const char *ip,int port,const char *pin,const char *iface,int *fd,uint64_t deadline,int send_sni)
{
    *fd=-1;if(port<1||port>65535)return NULL;
    int sock=socket(AF_INET,SOCK_STREAM|SOCK_CLOEXEC|SOCK_NONBLOCK,0);if(sock<0)return NULL;
    SSL_CTX *ctx=NULL;SSL *ssl=NULL;
    int yes=1;if(setsockopt(sock,IPPROTO_TCP,TCP_NODELAY,&yes,sizeof(yes)))goto fail;
    if(iface&&*iface&&setsockopt(sock,SOL_SOCKET,SO_BINDTODEVICE,iface,strlen(iface)+1))goto fail;
    struct sockaddr_in sa={0};sa.sin_family=AF_INET;sa.sin_port=htons((uint16_t)port);inet_pton(AF_INET,ip,&sa.sin_addr);
    if(connect(sock,(struct sockaddr *)&sa,sizeof(sa)) && errno!=EINPROGRESS)goto fail;
    if(wait_fd(sock,POLLOUT,deadline))goto fail;
    int error=0;socklen_t len=sizeof(error);if(getsockopt(sock,SOL_SOCKET,SO_ERROR,&error,&len)||error)goto fail;
    ctx=SSL_CTX_new(TLS_client_method());if(!ctx)goto fail;
    SSL_CTX_set_min_proto_version(ctx,TLS1_2_VERSION);SSL_CTX_set_options(ctx,SSL_OP_NO_COMPRESSION);
    if(*pin)SSL_CTX_set_verify(ctx,SSL_VERIFY_NONE,NULL);
    else {
        SSL_CTX_set_verify(ctx,SSL_VERIFY_PEER,NULL);
        if(SSL_CTX_set_default_verify_paths(ctx)!=1)goto fail;
        /* ASUS builds OpenSSL with OPENSSLDIR=/etc, while its existing CA
           bundle is installed at the same path used by firmware libcurl. */
        if(access("/etc/ssl/certs/ca-certificates.crt",R_OK)==0 &&
           SSL_CTX_load_verify_locations(ctx,"/etc/ssl/certs/ca-certificates.crt",NULL)!=1)goto fail;
    }
    ssl=SSL_new(ctx);if(!ssl)goto fail;SSL_CTX_free(ctx);ctx=NULL;
    SSL_set_fd(ssl,sock);
    if(send_sni && SSL_set_tlsext_host_name(ssl,host)!=1)goto fail;
    if(!*pin) {
        struct in_addr numeric;
        if(inet_pton(AF_INET,host,&numeric)==1){if(X509_VERIFY_PARAM_set1_ip_asc(SSL_get0_param(ssl),host)!=1)goto fail;}
        else if(SSL_set1_host(ssl,host)!=1)goto fail;
    }
    if(handshake(ssl,1,deadline))goto fail;
    if(*pin){X509 *cert=SSL_get_peer_certificate(ssl);int rc=vr_tls_pinned(cert,pin);X509_free(cert);if(rc)goto fail;}
    *fd=sock;return ssl;
fail:SSL_free(ssl);SSL_CTX_free(ctx);close(sock);return NULL;
}

SSL *vr_tls_connect_at(const char *host,const char *ip,int port,const char *pin,const char *iface,int *fd,uint64_t deadline)
{return connect_at(host,ip,port,pin,iface,fd,deadline,1);}

static SSL *connect_resolved(const char *host,int port,const char *pin,const vr_config *config,const char *iface,int *fd,uint64_t deadline,int send_sni)
{
    uint64_t now=vr_millis(),budget=deadline>now?deadline-now:0;
    char addresses[4][16];*fd=-1;int count=vr_resolve_config4_all(host,config,iface,addresses);
    if(!budget||count<1)return NULL;
    uint64_t end=vr_millis()+budget;
    for(int i=0;i<count;i++) {
        now=vr_millis();if(now>=end)break;
        SSL *ssl=connect_at(host,addresses[i],port,pin,iface,fd,now+(end-now)/(uint64_t)(count-i),send_sni);
        if(ssl)return ssl;
    }
    return NULL;
}

SSL *vr_tls_connect(const char *host,int port,const char *pin,const vr_config *config,const char *iface,int *fd,uint64_t deadline)
{return connect_resolved(host,port,pin,config,iface,fd,deadline,1);}

SSL *vr_tls_connect_relay(const char *host,int port,const char *pin,const vr_config *config,const char *iface,int *fd,uint64_t deadline)
{
    /* Match Probe ordinary relays: no domain SNI. Cloudflare Copilot entry
       points need SNI to select the service. Certificate identity checks and
       the carrier's private HMAC/ticket authentication remain independent. */
    int send_sni=!strncasecmp(host,"api_copilot_",12);
    return connect_resolved(host,port,pin,config,iface,fd,deadline,send_sni);
}

int vr_http_header(SSL *ssl,char *header,size_t cap,uint64_t deadline)
{
    for(size_t i=0;i+1<cap;i++) {
        if(vr_tls_io(ssl,0,header+i,1,deadline) || !header[i])return -1;
        if(i>=3&&!memcmp(header+i-3,"\r\n\r\n",4)){header[i+1]=0;return 0;}
    }
    return -1;
}

const char *vr_http_field(const char *header,const char *name,char *out,size_t cap)
{
    size_t key=strlen(name);const char *p=strstr(header,"\r\n");int found=0;*out=0;
    while(p && p[2] && p[2]!='\r') {
        p+=2;const char *end=strstr(p,"\r\n");if(!end)return NULL;
        if((size_t)(end-p)>key && !strncasecmp(p,name,key) && p[key]==':') {
            if(found++)return NULL;
            const char *value=p+key+1;while(value<end&&(*value==' '||*value=='\t'))value++;
            while(end>value&&(end[-1]==' '||end[-1]=='\t'))end--;
            if((size_t)(end-value)>=cap)return NULL;
            memcpy(out,value,(size_t)(end-value));out[end-value]=0;
        }
        p=strstr(p,"\r\n");
    }
    return found?out:NULL;
}

int vr_http_token(const char *header,const char *name,const char *token)
{
    char value[1024];if(!vr_http_field(header,name,value,sizeof(value)))return 0;
    const char *p=value;size_t n=strlen(token);
    while(*p){while(*p==' '||*p=='\t'||*p==',')p++;const char *end=strchr(p,',');if(!end)end=p+strlen(p);
        const char *trim=end;while(trim>p&&(trim[-1]==' '||trim[-1]=='\t'))trim--;
        if((size_t)(trim-p)==n&&!strncasecmp(p,token,n))return 1;
        p=*end?end+1:end;}
    return 0;
}
