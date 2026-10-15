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
struct mime_handler {
    char *pattern,*type,*headers;
    void (*input)(char *,FILE *,int,char *);
    void (*output)(char *,FILE *);
    void (*auth)(char *,char *,char *);
};
int do_ssl,ssl_stream_fd;
char *nvram_get(const char *key){return !strcmp(key,"lan_ifname")?"lo":NULL;}
static void input(char *path,FILE *stream,int n,char *boundary)
{(void)path;(void)boundary;while(n-->0 && fgetc(stream)!=EOF){}}
static void output(char *path,FILE *stream){(void)path;fputs("stock page",stream);}
static void auth(char *u,char *p,char *r){(void)u;(void)p;(void)r;}
struct mime_handler mime_handlers[]={{"**.asp*","text/html","",input,output,auth},{0}};
int main(int argc,char **argv)
{
    signal(SIGPIPE,SIG_IGN);
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
        FILE *stream=fdopen(client,"r+");char line[4096],method[16],path[512];int length=0,authenticated=0;
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
