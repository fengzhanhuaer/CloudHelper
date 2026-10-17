#define _GNU_SOURCE
/* Disposable host-side model of the exported stock handler ABI. Authentication
 * is deliberately outside the extension, just as in vendor httpd.c. */
#include <arpa/inet.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>
#include <openssl/ssl.h>
#ifdef CHVR_HIDDEN
#define HIDDEN __attribute__((visibility("hidden")))
#else
#define HIDDEN
#endif
struct mime_handler {
    char *pattern,*type,*headers;
    void (*input)(char *,FILE *,int,char *);
    void (*output)(char *,FILE *);
    void (*auth)(char *,char *,char *);
};
HIDDEN int do_ssl,ssl_stream_fd;
char *nvram_get(const char *key){return !strcmp(key,"lan_ifname")?"lo":NULL;}
static void input(char *path,FILE *stream,int n,char *boundary)
{(void)path;(void)boundary;while(n-->0 && fgetc(stream)!=EOF){}}
static void output(char *path,FILE *stream){(void)path;fputs("stock page",stream);}
static void auth(char *u,char *p,char *r){(void)u;(void)p;(void)r;}
HIDDEN struct mime_handler mime_handlers[]={{"**.asp*","text/html","",input,output,auth},{0}};
typedef struct {SSL *ssl;int fd;} tls_cookie;
static ssize_t tls_read(void *p,char *b,size_t n){return SSL_read(((tls_cookie *)p)->ssl,b,(int)n);}
static ssize_t tls_write(void *p,const char *b,size_t n){return SSL_write(((tls_cookie *)p)->ssl,b,(int)n);}
static int tls_close(void *p){tls_cookie *c=p;SSL_free(c->ssl);close(c->fd);free(c);return 0;}
int main(int argc,char **argv)
{
    signal(SIGPIPE,SIG_IGN);
    SSL_CTX *context=NULL;
    if(argc==4&&!strcmp(argv[1],"--tls")){
        context=SSL_CTX_new(TLS_server_method());
        if(!context||SSL_CTX_use_certificate_file(context,argv[2],SSL_FILETYPE_PEM)!=1||SSL_CTX_use_PrivateKey_file(context,argv[3],SSL_FILETYPE_PEM)!=1)return 4;
    }
    if(argc==2 && !strcmp(argv[1],"--check-bridge")){
        if(mime_handlers[0].auth!=auth || mime_handlers[0].input==input || mime_handlers[0].output==output)return 2;
        puts("stock handler extension active");return 0;
    }
    int server=socket(AF_INET,SOCK_STREAM,0),yes=1;
    setsockopt(server,SOL_SOCKET,SO_REUSEADDR,&yes,sizeof(yes));
    struct sockaddr_in address={0};address.sin_family=AF_INET;address.sin_addr.s_addr=htonl(INADDR_LOOPBACK);
    if(bind(server,(struct sockaddr *)&address,sizeof(address)) || listen(server,4))return 3;
    socklen_t n=sizeof(address);getsockname(server,(struct sockaddr *)&address,&n);
    printf("%d\n",ntohs(address.sin_port));fflush(stdout);
    for(;;){
        int client=accept(server,NULL,NULL);if(client<0)continue;
        FILE *stream=NULL;char line[4096],method[16],path[512];int length=0,authenticated=0;
        if(context){
            SSL *ssl=SSL_new(context);if(!ssl){close(client);continue;}
            SSL_set_fd(ssl,client);if(SSL_accept(ssl)!=1){SSL_free(ssl);close(client);continue;}
            tls_cookie *cookie=calloc(1,sizeof(*cookie));if(!cookie){SSL_free(ssl);close(client);continue;}
            cookie->ssl=ssl;cookie->fd=client;
            cookie_io_functions_t io={tls_read,tls_write,NULL,tls_close};
            stream=fopencookie(cookie,"r+",io);if(!stream)tls_close(cookie);
        }else stream=fdopen(client,"r+");
        struct timeval timeout={3,0};setsockopt(client,SOL_SOCKET,SO_RCVTIMEO,&timeout,sizeof(timeout));
        if(!stream) {close(client);continue;}
        setvbuf(stream,NULL,_IONBF,0);
        if(!fgets(line,sizeof(line),stream) || sscanf(line,"%15s /%511s",method,path)!=2){fclose(stream);continue;}
        while(fgets(line,sizeof(line),stream) && strcmp(line,"\r\n")) {
            if(!strncmp(line,"Content-Length: ",16))length=atoi(line+16);
            if(!strcmp(line,"Cookie: stock_session=fixture\r\n"))authenticated=1;
        }
        if(!authenticated){while(length-->0 && fgetc(stream)!=EOF){}fputs("HTTP/1.0 401 Unauthorized\r\n\r\nstock login",stream);fclose(stream);continue;}
        mime_handlers[0].input(path,stream,length,NULL);
        fputs("HTTP/1.0 200 OK\r\nContent-Type: text/html\r\n\r\n",stream);
        mime_handlers[0].output(path,stream);fclose(stream);
    }
}
