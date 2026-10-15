#include "chvr.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static int calls;
int __wrap_vr_dns_exchange(const char *server,const char *iface,const void *query,size_t qn,void *reply,size_t cap,size_t *rn,int tcp)
{
    (void)iface;(void)tcp;calls++;
    if(!strcmp(server,"192.0.2.53:53"))return -1;
    assert(!strcmp(server,"192.0.2.54:53")||!strcmp(server,"192.0.2.55:53"));
    return vr_dns_reply(query,qn,reply,cap,0,"203.0.113.5",rn);
}
int main(void)
{
    vr_config c;vr_config_default(&c);c.dns_doh[0]=c.dns_dot[0]=0;strcpy(c.dns_upstream,"192.0.2.53:53");
    const char *owned="# CloudHelper managed upstream\nserver=127.0.0.1#1053\n",*original="server=192.0.2.54\nserver=192.0.2.54\n";
    assert(!vr_atomic_write("/tmp/resolv.dnsmasq",owned,strlen(owned)));
    assert(!vr_atomic_write("/tmp/cloudhelper-dns.original",original,strlen(original)));
    char ip[16];assert(!vr_resolve_config4("example.com",&c,"",ip)&&!strcmp(ip,"203.0.113.5")&&calls==2);
    /* A newer vendor upstream takes precedence over the interception backup. */
    const char *latest="server=192.0.2.55\n";assert(!vr_atomic_write("/tmp/resolv.dnsmasq",latest,strlen(latest)));
    assert(!vr_resolve_config4("example.com",&c,"",ip)&&calls==4);
    unlink("/tmp/resolv.dnsmasq");unlink("/tmp/cloudhelper-dns.original");
    puts("PASS: ordinary failure -> original system DNS; own interception filtered; vendor update wins");return 0;
}
