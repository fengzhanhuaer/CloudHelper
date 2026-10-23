#define _GNU_SOURCE
#include "../src/platform.c"
#include <assert.h>

static const char *failed_key;
static const char *fixture_lan="192.168.51.1",*fixture_wan="192.168.50.51";
static const char *fixture_lan_if="br0",*fixture_wan_if="eth0",*fixture_unit="";
static const char *fixture_gateway="eth0";

int __wrap_execvp(const char *file,char *const args[])
{
    assert(!strcmp(file,"nvram")&&!strcmp(args[1],"get")&&args[2]&&!args[3]);
    const char *key=args[2],*value=NULL;
    if(!strcmp(key,"lan_ipaddr"))value=fixture_lan;
    else if(!strcmp(key,"lan_ifname"))value=fixture_lan_if;
    else if(!strcmp(key,"wan_primary"))value=fixture_unit;
    else if(!strcmp(key,"wan0_ipaddr")||!strcmp(key,"wan1_ipaddr"))value=fixture_wan;
    else if(!strcmp(key,"wan0_gw_ifname")||!strcmp(key,"wan1_gw_ifname"))value=fixture_gateway;
    else if(!strcmp(key,"wan0_ifname")||!strcmp(key,"wan1_ifname"))value=fixture_wan_if;
    assert(value);size_t n=strlen(value);assert(write(STDOUT_FILENO,value,n)==(ssize_t)n);
    assert(write(STDOUT_FILENO,"\n",1)==1);
    /* A failing child may still have written a perfectly plausible address. */
    _exit(failed_key&&!strcmp(key,failed_key)?7:0);
}
uint64_t vr_millis(void)
{struct timespec t;clock_gettime(CLOCK_MONOTONIC,&t);return (uint64_t)t.tv_sec*1000+t.tv_nsec/1000000;}

int main(void)
{
    vr_addresses current,out;assert(!vr_addresses_get(&current));
    const char *keys[]={"lan_ipaddr","lan_ifname","wan_primary","wan0_ipaddr","wan0_gw_ifname","wan0_ifname"};
    for(size_t i=0;i<sizeof(keys)/sizeof(*keys);i++) {
        fixture_gateway=!strcmp(keys[i],"wan0_ifname")?"":"eth0";
        failed_key=keys[i];out=current;
        assert(vr_addresses_get(&out)<0);
        assert(!memcmp(&out,&current,sizeof(out)));
    }
    failed_key=NULL;fixture_gateway="eth0";
    puts("PASS: every NVRAM child failure rejects partial snapshots without changing the caller");
#ifndef ADDRESS_READER_ONLY
    vr_address_watch watch={0};char detail[240];
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==0);
    /* Ignore bytes after string terminators, not just zero-initialized fixtures. */
    vr_addresses padded;memset(&padded,'X',sizeof(padded));
    strcpy(padded.lan,current.lan);strcpy(padded.wan,current.wan);
    strcpy(padded.lan_if,current.lan_if);strcpy(padded.wan_if,current.wan_if);
    assert(vr_addresses_check(&watch,&padded,&out,detail,sizeof(detail))==0);
    fixture_wan="";assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==1);
    fixture_wan=current.wan;assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==0);
    fixture_wan="192.168.50.52";
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==1);
    failed_key="wan0_ipaddr";
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))<0);
    assert(strstr(detail,failed_key)&&strstr(detail,"retained"));
    failed_key=NULL;
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==1);
    fixture_wan="192.168.50.53";
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==1);
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==2);
    assert(!strcmp(out.wan,fixture_wan)&&strstr(detail,"confirmed")&&strstr(detail,current.wan)&&strstr(detail,fixture_wan));
    current=out;
    fixture_wan="0.0.0.0";
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==1);
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==2&&!*out.wan);
    current=out;fixture_lan="192.168.53.1";fixture_lan_if="br1";
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==1);
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==2&&!strcmp(out.lan,fixture_lan));
    current=out;fixture_gateway="";fixture_wan_if="eth1";fixture_unit="1";
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==1);
    failed_key="wan1_ifname";assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))<0);
    failed_key=NULL;
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==1);
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))==2&&!strcmp(out.wan_if,"eth1"));
    current=out;fixture_wan="invalid";
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))<0&&strstr(detail,"wan1_ipaddr"));
    fixture_wan="";fixture_unit="invalid";
    assert(vr_addresses_check(&watch,&current,&out,detail,sizeof(detail))<0&&strstr(detail,"wan_primary"));
    puts("PASS: semantic snapshots, transient/alternating changes and failed reads retain bindings; persistent LAN/WAN/interface changes are confirmed");
#endif
    return 0;
}
