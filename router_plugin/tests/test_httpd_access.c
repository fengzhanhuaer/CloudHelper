#define _GNU_SOURCE
#include "httpd_access.h"
#include <assert.h>
#include <stdlib.h>

static FILE *table(void)
{
    FILE *f=tmpfile();assert(f);fputs("Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n",f);return f;
}
static void route(FILE *f,const char *dev,const char *dest,const char *gateway,
                  unsigned flags,const char *mask)
{
    assert(!fseek(f,0,SEEK_END));
    assert(fprintf(f,"%s %08x %08x %04x 0 0 0 %08x 0 0 0\n",dev,
        (unsigned)inet_addr(dest),(unsigned)inet_addr(gateway),flags,(unsigned)inet_addr(mask))>0);
}
static int access_from(FILE *f,const char *peer,const char *local)
{
    if(f)rewind(f);
    return chvr_httpd_access(inet_addr(local),inet_addr(peer),inet_addr("192.168.50.1"),inet_addr("255.255.255.0"),f);
}
int main(void)
{
    assert(access_from(NULL,"192.168.50.91","192.168.50.1"));
    assert(!access_from(NULL,"198.18.0.8","192.168.50.1"));
    FILE *f=table();
    route(f,"ppp0","0.0.0.0","1.2.3.4",3,"0.0.0.0");
    route(f,"chvr0","198.18.0.0","0.0.0.0",1,"255.254.0.0");
    route(f,"chvr0","192.168.52.0","0.0.0.0",1,"255.255.255.0");
    route(f,"chvr0","8.8.8.0","0.0.0.0",1,"255.255.255.0");
    assert(access_from(f,"198.18.0.8","192.168.50.1"));
    assert(access_from(f,"198.19.255.254","192.168.50.1"));
    assert(access_from(f,"192.168.52.172","192.168.50.1"));
    assert(!access_from(f,"8.8.8.8","192.168.50.1"));
    assert(!access_from(f,"198.20.0.8","192.168.50.1"));
    assert(!access_from(f,"192.168.1.79","192.168.50.1"));
    assert(!access_from(f,"198.18.0.8","192.168.1.79"));
    assert(!access_from(f,"192.168.50.91","192.168.1.79"));
    route(f,"eth0","198.18.0.8","0.0.0.0",1,"255.255.255.255");
    assert(!access_from(f,"198.18.0.8","192.168.50.1"));
    assert(access_from(f,"198.18.0.9","192.168.50.1"));fclose(f);
    const unsigned flags[]={0,3,0x201};
    for(size_t i=0;i<sizeof(flags)/sizeof(flags[0]);i++){
        f=table();route(f,"chvr0","10.0.0.0","0.0.0.0",flags[i],"255.0.0.0");
        assert(!access_from(f,"10.1.2.3","192.168.50.1"));fclose(f);
    }
    f=table();route(f,"chvr0","0.0.0.0","0.0.0.0",1,"0.0.0.0");
    assert(!access_from(f,"198.18.0.8","192.168.50.1"));fclose(f);
    f=table();route(f,"chvr0","198.18.0.0","0.0.0.0",1,"255.254.0.0");
    route(f,"tun0","198.18.0.0","0.0.0.0",1,"255.254.0.0");
    assert(!access_from(f,"198.18.0.8","192.168.50.1"));fclose(f);
    puts("PASS: LAN destination, authenticated gateway return route, longest prefix and denied sources");
    return 0;
}
