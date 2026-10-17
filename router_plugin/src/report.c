#include "platform.h"
#include <inttypes.h>
#include <stdio.h>
#include <string.h>
#include <sys/sysinfo.h>

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
