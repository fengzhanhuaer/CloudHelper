#include "chvr.h"
#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

/* Controller-generated rules may get a fresh updated_at on every fetch.
   Compare all operational fields without mutating either published snapshot.
   In particular, credentials, expiry, rule removal and array order still count. */
int vr_policy_equal(json_object *a,json_object *b)
{
    if(a==b)return 1;
    if(!a||!b||json_object_get_type(a)!=json_object_get_type(b))return 0;
    if(json_object_is_type(a,json_type_object)) {
        json_object_object_foreach(a,key,value) {
            json_object *other=NULL;
            if(!strcmp(key,"updated_at"))continue;
            if(!json_object_object_get_ex(b,key,&other)||!vr_policy_equal(value,other))return 0;
        }
        json_object_object_foreach(b,key2,value2) {
            (void)value2;json_object *other=NULL;
            if(strcmp(key2,"updated_at")&&!json_object_object_get_ex(a,key2,&other))return 0;
        }
        return 1;
    }
    if(json_object_is_type(a,json_type_array)) {
        size_t n=json_object_array_length(a);if(n!=(size_t)json_object_array_length(b))return 0;
        for(size_t i=0;i<n;i++)if(!vr_policy_equal(json_object_array_get_idx(a,i),json_object_array_get_idx(b,i)))return 0;
        return 1;
    }
    return !strcmp(json_object_to_json_string_ext(a,JSON_C_TO_STRING_PLAIN),json_object_to_json_string_ext(b,JSON_C_TO_STRING_PLAIN));
}

static int ip_match(const char *text,const char *ip)
{
    struct in_addr a,b;char s[64];
    if(!ip || !*ip || inet_pton(AF_INET,ip,&a)!=1 || vr_copy(s,sizeof(s),text))return 0;
    unsigned prefix=32;char *slash=strchr(s,'/');
    if(slash) {char *end;*slash++=0;long v=strtol(slash,&end,10);if(!*slash || *end || v<0 || v>32)return 0;prefix=(unsigned)v;}
    if(inet_pton(AF_INET,s,&b)!=1)return 0;
    uint32_t mask=prefix?0xffffffffu<<(32-prefix):0;
    return (ntohl(a.s_addr)&mask)==(ntohl(b.s_addr)&mask);
}

static int matches(const char *entry,const char *domain,const char *ip)
{
    const char *sep=strchr(entry,':');if(!sep || !sep[1])return 0;
    const char *v=sep+1;size_t kind=(size_t)(sep-entry),a=strlen(domain),b=strlen(v);
    if(kind==13 && !strncmp(entry,"domain_suffix",kind))return a>=b && !strcasecmp(domain+a-b,v) && (a==b || domain[a-b-1]=='.');
    if(kind==13 && !strncmp(entry,"domain_prefix",kind))return a>=b && !strncasecmp(domain,v,b);
    if(kind==14 && !strncmp(entry,"domain_keyword",kind)) {
        for(size_t i=0;i+b<=a;i++)if(!strncasecmp(domain+i,v,b))return 1;
        return 0;
    }
    if((kind==2 && !strncmp(entry,"ip",kind)) || (kind==4 && !strncmp(entry,"cidr",kind)) ||
       (kind==7 && (!strncmp(entry,"ip_cidr",kind) || !strncmp(entry,"ip-cidr",kind))))return ip_match(v,ip);
    return 0;
}

static int text_field(json_object *o,const char *field,size_t min,size_t max,int multiline)
{
    json_object *v=vr_member(o,field);const char *s=vr_string(o,field);size_t len=strlen(s);
    if(!v)return min==0;
    if(!json_object_is_type(v,json_type_string)||len!=(size_t)json_object_get_string_len(v)||len<min||len>max)return 0;
    for(size_t i=0;i<len;i++)if((unsigned char)s[i]<32 && !(multiline&&(s[i]=='\n'||s[i]=='\r')))return 0;
    return 1;
}

int vr_policy_needs_ip(json_object *policy,const char *domain)
{
    json_object *rules=vr_member(policy,"route_rules");
    for(size_t i=0;i<(size_t)json_object_array_length(rules);i++) {
        json_object *rule=json_object_array_get_idx(rules,i),*enabled=vr_member(rule,"enabled"),*a=vr_member(rule,"entries");
        if(enabled&&!json_object_get_boolean(enabled))continue;
        int ip_rule=0;
        for(size_t j=0;j<(size_t)json_object_array_length(a);j++) {
            const char *entry=json_object_get_string(json_object_array_get_idx(a,j));if(!entry)continue;
            if(matches(entry,domain,NULL))return 0;
            if(!strncmp(entry,"ip:",3)||!strncmp(entry,"cidr:",5)||!strncmp(entry,"ip_cidr:",8)||!strncmp(entry,"ip-cidr:",8))ip_rule=1;
        }
        if(ip_rule)return 1;
    }
    return 0;
}

vr_decision vr_policy_match(json_object *policy,const char *domain,const char *ip)
{
    vr_decision d={0};json_object *rules=vr_member(policy,"route_rules");
    if(!domain)domain="";
    size_t count=rules && json_object_is_type(rules,json_type_array)?json_object_array_length(rules):0;
    for(size_t i=0;i<count && i<VR_ROUTE_MAX;i++) {
        json_object *r=json_object_array_get_idx(rules,i),*enabled=vr_member(r,"enabled"),*entries=vr_member(r,"entries");
        if((enabled && !json_object_get_boolean(enabled)) || !entries || !json_object_is_type(entries,json_type_array))continue;
        size_t n=json_object_array_length(entries);
        for(size_t j=0;j<n && j<4096;j++) {
            json_object *e=json_object_array_get_idx(entries,j);
            if(!json_object_is_type(e,json_type_string) || !matches(json_object_get_string(e),domain,ip))continue;
            const char *action=vr_string(r,"action");
            d.action=!strcmp(action,"direct")?VR_DIRECT:!strcmp(action,"reject")?VR_REJECT:VR_PROXY;
            if(vr_copy(d.rule,sizeof(d.rule),vr_string(r,"id")) || vr_copy(d.exit_node,sizeof(d.exit_node),vr_string(r,"exit_node_id")) ||
               vr_copy(d.backup,sizeof(d.backup),vr_string(r,"backup_exit_node_id")))d.action=VR_REJECT;
            return d;
        }
    }
    return d;
}

int vr_policy_service_port(json_object *policy,const char *node_id,int fallback)
{
    json_object *nodes=vr_member(policy,"probe_ips");
    for(size_t i=0;i<(size_t)json_object_array_length(nodes);i++) {
        json_object *node=json_object_array_get_idx(nodes,i);
        if(!strcmp(vr_string(node,"node_id"),node_id)) {
            fallback=json_object_get_int(vr_member(node,"service_port"));break;
        }
    }
    return fallback>0 && fallback<=65535?fallback:12040;
}

int vr_policy_validate(json_object *s,const char *node_id,char *err,size_t n)
{
    const char *why="invalid controller configuration";
    if(strcmp(vr_string(s,"node_id"),node_id) || strcmp(vr_string(s,"expected_node_kind"),VR_KIND)) {
        why="controller node identity or type mismatch";goto fail;
    }
    json_object *p=vr_member(s,"virtual_router"),*nodes=vr_member(p,"probe_ips"),*routes=vr_member(p,"route_rules"),*links=vr_member(p,"topology_rules");
    if(!p || !json_object_is_type(nodes,json_type_array) || json_object_array_length(nodes)>256 ||
        !json_object_is_type(routes,json_type_array) || json_object_array_length(routes)>VR_ROUTE_MAX ||
        !json_object_is_type(links,json_type_array) || json_object_array_length(links)>512)goto fail;
    char cidr[64];if(vr_copy(cidr,sizeof(cidr),vr_string(p,"fake_ip_cidr")))goto fail;
    char *slash=strchr(cidr,'/');struct in_addr base;if(!slash)goto fail;*slash++=0;
    char *end;long prefix=strtol(slash,&end,10);
    if(!*slash || *end || prefix<15 || prefix>30 || inet_pton(AF_INET,cidr,&base)!=1 || (ntohl(base.s_addr)&0xfffe0000u)!=0xc6120000u)goto fail;
    size_t own=0,adjacent=0;
    for(size_t i=0;i<(size_t)json_object_array_length(nodes);i++) {
        json_object *node=json_object_array_get_idx(nodes,i);const char *id=vr_string(node,"node_id"),*ip=vr_string(node,"ip");
        if(!text_field(node,"node_id",1,128,0) || !ip_match(vr_string(p,"fake_ip_cidr"),ip))goto fail;
        for(size_t j=0;j<i;j++) {json_object *other=json_object_array_get_idx(nodes,j);
            if(!strcmp(id,vr_string(other,"node_id")) || !strcmp(ip,vr_string(other,"ip")))goto fail;}
        if(!strcmp(id,node_id))own++;
    }
    if(own!=1)goto fail;
    for(size_t i=0;i<(size_t)json_object_array_length(links);i++) {
        json_object *r=json_object_array_get_idx(links,i);
        if(!text_field(r,"id",1,128,0)||!text_field(r,"from_node_id",1,128,0)||!text_field(r,"to_node_id",1,128,0))goto fail;
        if(!json_object_get_boolean(vr_member(r,"enabled")))continue;
        if(strcmp(vr_string(r,"from_node_id"),node_id) && strcmp(vr_string(r,"to_node_id"),node_id))continue;
        if(++adjacent>VR_LINK_MAX){why="router supports at most 8 adjacent links";goto fail;}
        const char *layer=vr_string(r,"route_layer");
        if(*layer && strcmp(layer,"auto") && strcmp(layer,"http2") && strcmp(layer,"websocket")) {why="unsupported transport; select TLS/WebSocket";goto fail;}
        if(!text_field(r,"secret",1,1024,0)||!text_field(r,"auth_ticket",1,8192,0)||!text_field(r,"user_public_key",1,4096,1)||
           !text_field(r,"to_service_domain",0,253,0)||!text_field(r,"to_tls_spki_sha256",0,64,0))goto fail;
        int port=json_object_get_int(vr_member(r,"to_service_port"));if(port<0||port>65535)goto fail;
    }
    size_t entries=0;
    for(size_t i=0;i<(size_t)json_object_array_length(routes);i++) {
        json_object *r=json_object_array_get_idx(routes,i),*a=vr_member(r,"entries");
        const char *action=vr_string(r,"action");
        if(!text_field(r,"id",1,128,0)||!text_field(r,"exit_node_id",!strcmp(action,"probe_exit"),128,0)||!text_field(r,"backup_exit_node_id",0,128,0))goto fail;
        if(strcmp(action,"direct") && strcmp(action,"reject") && strcmp(action,"probe_exit"))goto fail;
        if(!a || !json_object_is_type(a,json_type_array) || json_object_array_length(a)>4096)goto fail;
        entries+=json_object_array_length(a);if(entries>16384)goto fail;
        for(size_t j=0;j<(size_t)json_object_array_length(a);j++) {
            json_object *v=json_object_array_get_idx(a,j);
            if(!json_object_is_type(v,json_type_string) || json_object_get_string_len(v)>512)goto fail;
        }
    }
    if(n)*err=0;
    return 0;
fail:snprintf(err,n,"%s",why);return -1;
}
