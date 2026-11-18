#include "platform.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
int main(void)
{
    vr_addresses a={0};strcpy(a.lan,"192.0.2.1");strcpy(a.lan_if,"lan0");
    vr_dns_filter f={0};const char ips[1][46]={"203.0.113.53"};char command[32];
    puts("READY");fflush(stdout);
    while(fgets(command,sizeof(command),stdin)) {
        if(!strncmp(command,"stop",4))break;
        f.block_doh=f.block_dot=f.redirect_plain=!strncmp(command,"on",2);
        assert(!vr_platform_dns_filter(&f,&a,ips,1));puts("APPLIED");fflush(stdout);
    }
    vr_platform_dns_filter_stop();return 0;
}
