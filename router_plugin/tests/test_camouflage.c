#define _GNU_SOURCE
#include "platform.h"
#include "transport.h"
#include <assert.h>
#include <netinet/in.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <sys/socket.h>
#include <unistd.h>

int main(int argc,char **argv)
{
    assert(argc==3);signal(SIGPIPE,SIG_IGN);char cert[8192],key[8192];size_t cn,kn;
    assert(!vr_read_file(argv[1],cert,sizeof(cert)-1,&cn)&&!vr_read_file(argv[2],key,sizeof(key)-1,&kn));cert[cn]=0;key[kn]=0;
    SSL_CTX *ctx=vr_tls_server_context(cert,key);assert(ctx);int server=socket(AF_INET,SOCK_STREAM|SOCK_CLOEXEC,0);assert(server>=0);
    struct sockaddr_in address={0};address.sin_family=AF_INET;address.sin_addr.s_addr=htonl(INADDR_LOOPBACK);
    assert(!bind(server,(struct sockaddr *)&address,sizeof(address))&&!listen(server,4));
    socklen_t n=sizeof(address);assert(!getsockname(server,(struct sockaddr *)&address,&n));
    printf("READY %u\n",ntohs(address.sin_port));fflush(stdout);
    assert(!vr_http_camouflage(NULL,"GET " VR_RELAY_PATH "?route_id=private HTTP/1.1\r\n\r\n",0));
    for(unsigned i=0;i<5;i++) {
        struct pollfd p={server,POLLIN,0};assert(poll(&p,1,10000)>0);int fd=accept4(server,NULL,NULL,SOCK_CLOEXEC);assert(fd>=0);
        uint64_t end=vr_millis()+4000;SSL *ssl=vr_tls_accept(ctx,fd,end);assert(ssl);char request[4096];
        assert(!vr_http_header(ssl,request,sizeof(request),end));assert(vr_http_camouflage(ssl,request,end)==1);SSL_free(ssl);close(fd);
    }
    close(server);SSL_CTX_free(ctx);return 0;
}
