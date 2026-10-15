#define _GNU_SOURCE
#include "platform.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

int main(void)
{
    char normalized[19];assert(!vr_private_cidr("192.168.50.7/24",normalized)&&!strcmp(normalized,"192.168.50.0/24"));
    const char *bad[]={"0.0.0.0/0","172.16.0.0/8","192.168.0.0/8","198.18.0.0/15","127.0.0.1/32"};
    for(size_t i=0;i<sizeof(bad)/sizeof(bad[0]);i++)assert(vr_private_cidr(bad[i],normalized));
    const char *raw="{\"enabled\":true,\"proxy_cidrs\":[\"192.168.50.7/24\",\"10.20.0.0/16\"],\"allowed_node_ids\":[\"7\",\"14\"]}";
    json_object *o=vr_json_parse(raw,strlen(raw));vr_config c;vr_config_default(&c);assert(!vr_gateway_parse(&c.gateway,o));json_object_put(o);
    assert(vr_gateway_allowed(&c.gateway,"7")&&!vr_gateway_allowed(&c.gateway,"9")&&vr_gateway_contains(&c.gateway,"10.20.1.1")&&!vr_gateway_contains(&c.gateway,"10.21.1.1"));
    char root[]="/tmp/chvr-gateway-XXXXXX";assert(mkdtemp(root));char path[512];snprintf(path,sizeof(path),"%s/config.json",root);assert(!vr_config_save(&c,path));
    vr_config restored;char error[256];assert(!vr_config_load(&restored,path,error,sizeof(error)));assert(restored.gateway.enabled&&restored.gateway.cidr_count==2&&restored.gateway.node_count==2);
    struct stat before,after;assert(!stat(path,&before));assert(!vr_config_save(&restored,path));assert(!stat(path,&after)&&before.st_ino==after.st_ino);
    unlink(path);
    snprintf(path,sizeof(path),"%s/wan0",root);assert(!mkdir(path,0700));snprintf(path,sizeof(path),"%s/wan0/statistics",root);assert(!mkdir(path,0700));
    snprintf(path,sizeof(path),"%s/wan0/statistics/rx_bytes",root);assert(!vr_atomic_write(path,"123456789012\n",13));
    snprintf(path,sizeof(path),"%s/wan0/statistics/tx_bytes",root);assert(!vr_atomic_write(path,"9876543210\n",11));
    vr_addresses a={0};strcpy(a.lan,"192.168.52.1");strcpy(a.wan,"192.168.205.2");strcpy(a.wan_if,"wan0");
    o=vr_probe_report(&c,&a,root);json_object *system=vr_member(o,"system");
    assert(json_object_array_length(vr_member(o,"ipv4"))==2&&!strcmp(vr_string(o,"lan_ip"),a.lan)&&!strcmp(vr_string(o,"wan_ip"),a.wan));
    assert(json_object_get_boolean(vr_member(system,"network_traffic_available"))&&json_object_get_int64(vr_member(system,"network_rx_bytes"))==123456789012LL&&json_object_get_int64(vr_member(system,"network_tx_bytes"))==9876543210LL);json_object_put(o);
    strcpy(a.wan,a.lan);o=vr_probe_report(&c,&a,root);assert(json_object_array_length(vr_member(o,"ipv4"))==1);json_object_put(o);
    *a.wan=0;o=vr_probe_report(&c,&a,root);assert(json_object_array_length(vr_member(o,"ipv4"))==1&&!json_object_get_boolean(vr_member(vr_member(o,"system"),"network_traffic_available")));json_object_put(o);
    unlink(path);snprintf(path,sizeof(path),"%s/wan0/statistics/rx_bytes",root);unlink(path);snprintf(path,sizeof(path),"%s/wan0/statistics",root);rmdir(path);snprintf(path,sizeof(path),"%s/wan0",root);rmdir(path);rmdir(root);
    puts("PASS: gateway validation/persistence/ACL, WAN/LAN reporting and WAN-only 64-bit traffic counters");return 0;
}
