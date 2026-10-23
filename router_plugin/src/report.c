#define _GNU_SOURCE
#include "platform.h"
#include <arpa/inet.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <inttypes.h>
#include <stdio.h>
#include <string.h>
#include <sys/sysinfo.h>

int vr_public_ip(const char *text,int family,char *out,size_t cap)
{
    char value[64];size_t n=strcspn(text," \t\r\n");
    if(!n||n>=sizeof(value))return -1;
    for(const char *p=text+n;*p;p++)if(*p!=' '&&*p!='\t'&&*p!='\r'&&*p!='\n')return -1;
    memcpy(value,text,n);value[n]=0;uint8_t b[16];
    if(inet_pton(family,value,b)!=1)return -1;
    if(family==AF_INET) {
        if(!b[0]||b[0]==10||b[0]==127||b[0]>=224||(b[0]==169&&b[1]==254)||
           (b[0]==172&&b[1]>=16&&b[1]<=31)||(b[0]==192&&b[1]==168)||
           (b[0]==100&&b[1]>=64&&b[1]<=127)||(b[0]==198&&(b[1]==18||b[1]==19)))return -1;
    } else if(family!=AF_INET6||(b[0]&0xe0)!=0x20)return -1;
    return inet_ntop(family,b,out,(socklen_t)cap)?0:-1;
}
static void add_ip(json_object *list,const char *ip)
{
    if(!*ip)return;
    for(size_t i=0;i<(size_t)json_object_array_length(list);i++)if(!strcmp(json_object_get_string(json_object_array_get_idx(list,i)),ip))return;
    json_object_array_add(list,json_object_new_string(ip));
}
int vr_report_addresses(json_object *o,const char *public4,const char *public6)
{
    json_object *v4=vr_member(o,"ipv4"),*v6=json_object_new_array();int has6=0;struct ifaddrs *addresses=NULL;
    if(!getifaddrs(&addresses)) {
        for(struct ifaddrs *a=addresses;a;a=a->ifa_next) {
            if(!a->ifa_addr||!(a->ifa_flags&IFF_UP)||(a->ifa_flags&IFF_LOOPBACK)||!strncmp(a->ifa_name,"chvr",4))continue;
            char ip[46];int family=a->ifa_addr->sa_family;
            if(family==AF_INET6) {
                struct sockaddr_in6 *sa=(struct sockaddr_in6 *)a->ifa_addr;
                if(inet_ntop(family,&sa->sin6_addr,ip,sizeof(ip))&&!vr_public_ip(ip,family,ip,sizeof(ip))){add_ip(v6,ip);has6=1;}
            }
        }
        freeifaddrs(addresses);
    }
    char ip[46];if(!vr_public_ip(public4,AF_INET,ip,sizeof(ip)))add_ip(v4,ip);
    if(has6&&!vr_public_ip(public6,AF_INET6,ip,sizeof(ip)))add_ip(v6,ip);
    json_object_object_add(o,"ipv6",v6);return has6;
}

static int counter(const char *root,const char *iface,const char *key,uint64_t *value)
{
    if(!*iface)return -1;
    for(const char *p=iface;*p;p++)if(!((*p>='a'&&*p<='z')||(*p>='A'&&*p<='Z')||(*p>='0'&&*p<='9')||*p=='_'||*p=='-'||*p=='.'))return -1;
    char path[512];if(snprintf(path,sizeof(path),"%s/%s/statistics/%s_bytes",root,iface,key)>=(int)sizeof(path))return -1;
    FILE *f=fopen(path,"r");if(!f)return -1;char tail;int ok=fscanf(f,"%" SCNu64 " %c",value,&tail)==1;fclose(f);return ok?0:-1;
}
json_object *vr_probe_report(const vr_config *c,const vr_addresses *a,const char *net_root)
{
    json_object *o=json_object_new_object(),*ips=json_object_new_array(),*system=json_object_new_object();
    json_object_object_add(o,"type",json_object_new_string("report"));json_object_object_add(o,"node_id",json_object_new_string(c->node_id));
    json_object_object_add(o,"platform",json_object_new_string("desktop"));json_object_object_add(o,"os",json_object_new_string("linux"));
    json_object_object_add(o,"arch",json_object_new_string("arm"));json_object_object_add(o,"build_kind",json_object_new_string(VR_KIND));
    json_object_object_add(o,"version",json_object_new_string(VR_VERSION));
    json_object_object_add(o,"router_plugin_upgrade",json_object_new_boolean(1));
    json_object_object_add(o,"router_plugin_carrier_slots",json_object_new_int(VR_CARRIER_SLOTS));
    if(*a->lan)json_object_array_add(ips,json_object_new_string(a->lan));
    if(*a->wan&&strcmp(a->wan,a->lan))json_object_array_add(ips,json_object_new_string(a->wan));
    json_object_object_add(o,"ipv4",ips);json_object_object_add(o,"lan_ip",json_object_new_string(a->lan));
    json_object_object_add(o,"wan_ip",json_object_new_string(a->wan));
    uint64_t rx=0,tx=0;int available=*a->wan&&!counter(net_root,a->wan_if,"rx",&rx)&&!counter(net_root,a->wan_if,"tx",&tx);
    json_object_object_add(system,"network_traffic_available",json_object_new_boolean(available));
    if(available){json_object_object_add(system,"network_rx_bytes",json_object_new_int64((int64_t)rx));json_object_object_add(system,"network_tx_bytes",json_object_new_int64((int64_t)tx));}
    struct sysinfo info;if(!sysinfo(&info)) {
        json_object_object_add(o,"machine_uptime_seconds",json_object_new_int64(info.uptime));
        json_object_object_add(system,"memory_total_bytes",json_object_new_int64((int64_t)info.totalram*info.mem_unit));
        json_object_object_add(system,"memory_used_bytes",json_object_new_int64((int64_t)(info.totalram-info.freeram-info.bufferram)*info.mem_unit));
    }
    json_object_object_add(o,"system",system);json_object_object_add(o,"gateway_proxy",vr_gateway_json(&c->gateway));return o;
}
