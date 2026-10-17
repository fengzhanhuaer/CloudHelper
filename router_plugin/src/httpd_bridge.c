#define _GNU_SOURCE
/* Optional stock-httpd extension. Its authenticated ASP handler retains the
 * vendor's authentication callback; only this plugin's exact API is relayed.
 * No listener, shell command, disk spool or persistent log is introduced. */
#include <arpa/inet.h>
#include <dlfcn.h>
#include <errno.h>
#include <net/if.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>
#include "../include/httpd_layout.h"

#ifndef CHVR_ADMIN_SOCKET
#define CHVR_ADMIN_SOCKET "/var/run/cloudhelper-router/admin.sock"
#endif
#define API "user/cloudhelper-router/api.asp"
struct mime_handler {
    char *pattern, *type, *headers;
    void (*input)(char *, FILE *, int, char *);
    void (*output)(char *, FILE *);
    void (*auth)(char *, char *, char *);
};
static struct mime_handler *handler;
static void (*original_input)(char *, FILE *, int, char *);
static void (*original_output)(char *, FILE *);
static char body[16384];
static size_t body_size;
static int *ssl_enabled, *ssl_fd;
static char *(*nvram_value)(const char *);

static __thread void *active_ssl;
static __thread int active_ssl_fd=-1;
/* Track the current TLS stream via the public OpenSSL ABI. */
int SSL_read(void *ssl,void *buffer,int length)
{
    int (*read_ssl)(void *,void *,int)=dlsym(RTLD_NEXT,"SSL_read");
    int (*get_fd)(const void *)=dlsym(RTLD_NEXT,"SSL_get_fd");
    if(!read_ssl||!get_fd)return -1;
    active_ssl=ssl;active_ssl_fd=get_fd(ssl);return read_ssl(ssl,buffer,length);
}
void SSL_free(void *ssl)
{
    void (*free_ssl)(void *)=dlsym(RTLD_NEXT,"SSL_free");
    if(active_ssl==ssl){active_ssl=NULL;active_ssl_fd=-1;}
    if(free_ssl)free_ssl(ssl);
}

static struct mime_handler *hidden_stock_handler(void)
{
    chvr_httpd_layout layout;if(chvr_httpd_file("/proc/self/exe",&layout))return NULL;
    struct mime_handler *asp=(struct mime_handler *)layout.asp;
    if(sizeof(*asp)!=6*sizeof(uintptr_t) || !chvr_handler_matches(asp,layout.fields) ||
       strcmp(asp->pattern,"**.asp*"))return NULL;
    return asp;
}

static int api_path(const char *path) { return !strcmp(path, API); }
static void clear_body(void)
{
    volatile unsigned char *p=(volatile unsigned char *)body;
    for(size_t i=0;i<sizeof(body);i++)p[i]=0;
    body_size=0;
}
static int lan_client(FILE *stream)
{
    int fd=fileno(stream);
    if(ssl_enabled&&*ssl_enabled)fd=ssl_fd?*ssl_fd:-1;
    else if(fd<0)fd=active_ssl_fd;
    struct sockaddr_in peer,local;socklen_t n=sizeof(peer);
    if(fd<0 || getpeername(fd,(struct sockaddr *)&peer,&n) || peer.sin_family!=AF_INET)return 0;
    n=sizeof(local);
    if(getsockname(fd,(struct sockaddr *)&local,&n) || local.sin_family!=AF_INET)return 0;
    const char *name=nvram_value ? nvram_value("lan_ifname") : NULL;
    if(!name || !*name || strlen(name)>=IFNAMSIZ)return 0;
    int query=socket(AF_INET,SOCK_DGRAM|SOCK_CLOEXEC,0);if(query<0)return 0;
    struct ifreq request;memset(&request,0,sizeof(request));strcpy(request.ifr_name,name);
    int valid=0;
    if(!ioctl(query,SIOCGIFADDR,&request)) {
        in_addr_t address=((struct sockaddr_in *)&request.ifr_addr)->sin_addr.s_addr;
        if(local.sin_addr.s_addr==address && !ioctl(query,SIOCGIFNETMASK,&request)) {
            in_addr_t mask=((struct sockaddr_in *)&request.ifr_netmask)->sin_addr.s_addr;
            valid=(peer.sin_addr.s_addr&mask)==(address&mask);
        }
    }
    close(query);return valid;
}
static void bridge_input(char *path,FILE *stream,int length,char *boundary)
{
    if(!api_path(path)){original_input(path,stream,length,boundary);return;}
    clear_body();
    if(length<=0)return;
    if(length>(int)sizeof(body) || !lan_client(stream)) {
        /* Drain modest invalid requests so the stock connection can return an
         * error cleanly. Do not process an unbounded declared upload. */
        for(int i=0;i<length && i<32768;i++)if(fgetc(stream)==EOF)break;
        return;
    }
    for(int i=0;i<length;i++) {
        int c=fgetc(stream);if(c==EOF || c==0){clear_body();return;}
        body[i]=(char)c;
    }
    body_size=(size_t)length;
}
static int send_all(int fd,const char *data,size_t length)
{
    while(length){ssize_t n=send(fd,data,length,MSG_NOSIGNAL);if(n<0&&errno==EINTR)continue;if(n<=0)return -1;data+=n;length-=(size_t)n;}
    return 0;
}
static void bridge_output(char *path,FILE *stream)
{
    if(!api_path(path)){original_output(path,stream);return;}
    int fd=-1;FILE *local=NULL;char buffer[4096];
    if(!body_size || !lan_client(stream))goto fail;
    fd=socket(AF_UNIX,SOCK_STREAM|SOCK_CLOEXEC,0);if(fd<0)goto fail;
    struct timeval timeout={3,0};
    setsockopt(fd,SOL_SOCKET,SO_RCVTIMEO,&timeout,sizeof(timeout));
    setsockopt(fd,SOL_SOCKET,SO_SNDTIMEO,&timeout,sizeof(timeout));
    struct sockaddr_un address={0};address.sun_family=AF_UNIX;
    strcpy(address.sun_path,CHVR_ADMIN_SOCKET);
    if(connect(fd,(struct sockaddr *)&address,sizeof(address)))goto fail;
    struct ucred peer;socklen_t size=sizeof(peer);
    if(getsockopt(fd,SOL_SOCKET,SO_PEERCRED,&peer,&size) || peer.uid!=geteuid())goto fail;
    int n=snprintf(buffer,sizeof(buffer),"POST / HTTP/1.0\r\nContent-Length: %zu\r\n\r\n",body_size);
    if(send_all(fd,buffer,(size_t)n) || send_all(fd,body,body_size))goto fail;
    clear_body();local=fdopen(fd,"r");if(!local)goto fail;
    if(!fgets(buffer,sizeof(buffer),local) || strcmp(buffer,"HTTP/1.0 200 OK\r\n"))goto fail;
    size_t header_size=0,length=0;int lengths=0,complete=0;
    while(fgets(buffer,sizeof(buffer),local)) {
        header_size+=strlen(buffer);if(header_size>4096)goto fail;
        if(!strcmp(buffer,"\r\n")){complete=1;break;}
        if(!strncmp(buffer,"Content-Length: ",16)) {
            char tail;unsigned long value;
            if(sscanf(buffer+16,"%lu %c",&value,&tail)!=1 || !value || value>524288 || ++lengths>1)goto fail;
            length=(size_t)value;
        }
    }
    if(!complete || lengths!=1)goto fail;
    while(length) {
        size_t want=length<sizeof(buffer)?length:sizeof(buffer);
        size_t got=fread(buffer,1,want,local);if(!got)break;
        if(fwrite(buffer,1,got,stream)!=got)break;
        length-=got;
    }
    fclose(local);return;
fail:
    clear_body();if(local)fclose(local);else if(fd>=0)close(fd);
    fputs("{\"ok\":false,\"error\":\"Virtual router service unavailable\"}",stream);
}
__attribute__((constructor)) static void attach(void)
{
    struct mime_handler *table=dlsym(RTLD_DEFAULT,"mime_handlers");
    ssl_enabled=dlsym(RTLD_DEFAULT,"do_ssl");ssl_fd=dlsym(RTLD_DEFAULT,"ssl_stream_fd");
    nvram_value=dlsym(RTLD_DEFAULT,"nvram_get");
    if(!nvram_value)return;
    if(!table) {
        struct mime_handler *asp=hidden_stock_handler();
        if(!asp)return;
        handler=asp;original_input=asp->input;original_output=asp->output;
        asp->input=bridge_input;asp->output=bridge_output;return;
    }
    for(unsigned i=0;i<256 && table[i].pattern;i++) {
        if(strcmp(table[i].pattern,"**.asp*"))continue;
        if(!table[i].auth || !table[i].input || !table[i].output)return;
        handler=&table[i];original_input=handler->input;original_output=handler->output;
        handler->input=bridge_input;handler->output=bridge_output;
        return;
    }
}
__attribute__((destructor)) static void detach(void)
{
    if(handler){handler->input=original_input;handler->output=original_output;}
    clear_body();
}
