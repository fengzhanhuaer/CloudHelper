#ifndef CHVR_HTTPD_ACCESS_H
#define CHVR_HTTPD_ACCESS_H

#include <arpa/inet.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

/* A private address alone is not sufficient. The established TCP client's
 * return path must use our authenticated packet gateway, not a WAN/VPN/default
 * route. /proc/net/route exposes the main table in native IPv4 byte order. */
static int chvr_httpd_gateway(FILE *routes, in_addr_t peer)
{
    uint32_t ip=ntohl(peer);
    if(!routes || !((ip>>24)==10 || (ip>>20)==0xac1 ||
       (ip>>16)==0xc0a8 || (ip>>17)==0x6309))return 0;
    char line[512],device[16];unsigned destination,gateway,flags,mask;
    uint32_t best=0;int allowed=0;
    if(!fgets(line,sizeof(line),routes))return 0;
    while(fgets(line,sizeof(line),routes)) {
        if(sscanf(line,"%15s %x %x %x %*u %*u %*u %x",device,
                  &destination,&gateway,&flags,&mask)!=5 || !(flags&1))continue;
        uint32_t host_mask=ntohl(mask),inverse=~host_mask;
        if((inverse&(inverse+1)) || (peer&mask)!=(destination&mask) || host_mask<best)continue;
        int accept=host_mask && !gateway && !(flags&(2|0x200)) && !strcmp(device,"chvr0");
        /* An equally specific competing route must not expand access. */
        allowed=host_mask==best ? allowed&&accept : accept;best=host_mask;
    }
    return !ferror(routes) && allowed;
}

static int chvr_httpd_access(in_addr_t local,in_addr_t peer,
                            in_addr_t lan,in_addr_t mask,FILE *routes)
{
    return local==lan && ((peer&mask)==(lan&mask) || chvr_httpd_gateway(routes,peer));
}
#endif
