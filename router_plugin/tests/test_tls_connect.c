#include "transport.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

int __wrap_vr_resolve_config4_all(const char *host,const vr_config *config,const char *iface,char out[4][16])
{(void)host;(void)config;(void)iface;strcpy(out[0],"127.0.0.1");return 1;}

int main(int argc,char **argv)
{
    assert(argc==5);int fd=-1;vr_config config={0};
    SSL *ssl=!strcmp(argv[1],"relay")?
        vr_tls_connect_relay(argv[2],atoi(argv[3]),argv[4],&config,NULL,&fd,vr_millis()+3000):
        vr_tls_connect(argv[2],atoi(argv[3]),argv[4],&config,NULL,&fd,vr_millis()+3000);
    if(!ssl){assert(fd==-1);puts("rejected");return 2;}
    assert(fd>=0);SSL_free(ssl);close(fd);puts("accepted");return 0;
}
