#include "engine.h"
#include <string.h>
#include <unistd.h>

int vr_addresses_apply(vr_engine *e,const vr_addresses *next)
{
    vr_addresses old=vr_engine_addresses(e);
    int lan=-1,wan=-1,sniff=-1;
    int move_lan=strcmp(old.lan,next->lan)!=0;
    int move_wan=e->config.allow_wan&&strcmp(old.wan,next->wan)!=0;
    /* Prepare every replacement before touching current listeners or rules. */
    if(move_lan) {
        lan=vr_bind4(next->lan,e->config.port,1);if(lan<0)goto fail;
        if(vr_tcp_listener_active(e)) {sniff=vr_bind4(next->lan,1054,1);if(sniff<0)goto fail;}
    }
    if(move_wan&&*next->wan) {wan=vr_bind4(next->wan,e->config.port,1);if(wan<0)goto fail;}
    if(vr_platform_readdress(&e->config,&old,next,e->cidr))goto fail;
    pthread_mutex_lock(&e->mutex);e->addresses=*next;pthread_mutex_unlock(&e->mutex);
    if(move_lan) {int fd=e->lan;e->lan=lan;lan=-1;if(fd>=0)close(fd);}
    if(move_wan) {int fd=e->wan;e->wan=wan;wan=-1;if(fd>=0)close(fd);}
    if(sniff>=0){vr_tcp_listener_replace(e,sniff);sniff=-1;}
    return 0;
fail:
    if(lan>=0)close(lan);
    if(wan>=0)close(wan);
    if(sniff>=0)close(sniff);
    return -1;
}
