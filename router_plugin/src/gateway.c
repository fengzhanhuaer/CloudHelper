#include "chvr.h"
#include <arpa/inet.h>
#include <ctype.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int vr_private_cidr(const char *raw,char normalized[19])
{
    char b[64];if(vr_copy(b,sizeof(b),raw))return -1;
    char *slash=strchr(b,'/'),*end;if(!slash)return -1;*slash++=0;
    long bits=strtol(slash,&end,10);struct in_addr a;
    if(!*slash||*end||bits<8||bits>32||inet_pton(AF_INET,b,&a)!=1)return -1;
    uint32_t v=ntohl(a.s_addr)&(0xffffffffu<<(32-bits));
    if(!((bits>=8&&(v&0xff000000u)==0x0a000000u)||
         (bits>=12&&(v&0xfff00000u)==0xac100000u)||
         (bits>=16&&(v&0xffff0000u)==0xc0a80000u)))return -1;
    a.s_addr=htonl(v);char ip[16];inet_ntop(AF_INET,&a,ip,sizeof(ip));
    snprintf(normalized,19,"%s/%ld",ip,bits);return 0;
}
int vr_cidr_contains(const char *raw,const char *ip)
{
    char b[64];if(vr_copy(b,sizeof(b),raw))return 0;
    char *slash=strchr(b,'/'),*end;if(!slash)return 0;*slash++=0;
    long bits=strtol(slash,&end,10);struct in_addr a,target;
    if(!*slash||*end||bits<0||bits>32||inet_pton(AF_INET,b,&a)!=1||inet_pton(AF_INET,ip,&target)!=1)return 0;
    uint32_t mask=bits?0xffffffffu<<(32-bits):0;
    return (ntohl(a.s_addr)&mask)==(ntohl(target.s_addr)&mask);
}
int vr_gateway_parse(vr_gateway *g,json_object *o)
{
    vr_gateway next={0};json_object *enabled=vr_member(o,"enabled"),*cidrs=vr_member(o,"proxy_cidrs"),*nodes=vr_member(o,"allowed_node_ids");
    if(!o||!json_object_is_type(o,json_type_object)||json_object_object_length(o)!=3||
       !json_object_is_type(enabled,json_type_boolean)||!json_object_is_type(cidrs,json_type_array)||!json_object_is_type(nodes,json_type_array)||
       json_object_array_length(cidrs)>VR_GATEWAY_CIDRS||json_object_array_length(nodes)>VR_GATEWAY_NODES)return -1;
    next.enabled=json_object_get_boolean(enabled);
    for(size_t i=0;i<(size_t)json_object_array_length(cidrs);i++) {
        json_object *v=json_object_array_get_idx(cidrs,i);char normalized[19];
        if(!json_object_is_type(v,json_type_string)||strlen(json_object_get_string(v))!=(size_t)json_object_get_string_len(v)||vr_private_cidr(json_object_get_string(v),normalized))return -1;
        size_t at;for(at=0;at<next.cidr_count;at++)if(!strcmp(normalized,next.cidrs[at]))break;
        if(at==next.cidr_count)strcpy(next.cidrs[next.cidr_count++],normalized);
    }
    for(size_t i=0;i<(size_t)json_object_array_length(nodes);i++) {
        json_object *v=json_object_array_get_idx(nodes,i);const char *id=json_object_get_string(v);
        if(!json_object_is_type(v,json_type_string)||!id||!*id||strlen(id)>128||strlen(id)!=(size_t)json_object_get_string_len(v))return -1;
        for(const unsigned char *p=(const unsigned char *)id;*p;p++)if(!(isalnum(*p)||*p=='_'||*p=='-'||*p=='.'))return -1;
        size_t at;for(at=0;at<next.node_count;at++)if(!strcmp(id,next.nodes[at]))break;
        if(at==next.node_count)strcpy(next.nodes[next.node_count++],id);
    }
    if(next.enabled&&(!next.cidr_count||!next.node_count))return -1;
    *g=next;return 0;
}
json_object *vr_gateway_json(const vr_gateway *g)
{
    json_object *o=json_object_new_object(),*cidrs=json_object_new_array(),*nodes=json_object_new_array();
    json_object_object_add(o,"enabled",json_object_new_boolean(g->enabled));
    for(size_t i=0;i<g->cidr_count;i++)json_object_array_add(cidrs,json_object_new_string(g->cidrs[i]));
    for(size_t i=0;i<g->node_count;i++)json_object_array_add(nodes,json_object_new_string(g->nodes[i]));
    json_object_object_add(o,"proxy_cidrs",cidrs);json_object_object_add(o,"allowed_node_ids",nodes);return o;
}
int vr_gateway_contains(const vr_gateway *g,const char *ip)
{for(size_t i=0;i<g->cidr_count;i++)if(vr_cidr_contains(g->cidrs[i],ip))return 1;return 0;}
int vr_gateway_allowed(const vr_gateway *g,const char *node)
{if(!g->enabled)return 0;for(size_t i=0;i<g->node_count;i++)if(!strcmp(g->nodes[i],node))return 1;return 0;}
