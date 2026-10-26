#include "chvr.h"
#include <arpa/inet.h>
#include <ctype.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>


void vr_config_default(vr_config *c)
{
    memset(c,0,sizeof(*c)); c->port=12040; c->dns_enabled=1;
    c->doh_enabled=c->dot_enabled=c->plain_dns_enabled=1;
    strcpy(c->dns_upstream,"223.5.5.5:53");
    strcpy(c->dns_doh,"https://dns.alidns.com/dns-query");strcpy(c->dns_dot,"dns.alidns.com:853");
}

static int string_field(json_object *o, const char *key, char *dst, size_t n)
{
    json_object *v=vr_member(o,key);
    if (!v) return 0;
    if (!json_object_is_type(v,json_type_string)) return -1;
    const char *s=json_object_get_string(v);
    if ((size_t)json_object_get_string_len(v)!=strlen(s)) return -1;
    for (const unsigned char *p=(const unsigned char *)s; *p; p++) if (*p<32 || *p==127) return -1;
    return vr_copy(dst,n,s);
}

static int boolean_field(json_object *o, const char *key, int *dst)
{
    json_object *v=vr_member(o,key);
    if (!v) return 0;
    if (!json_object_is_type(v,json_type_boolean)) return -1;
    *dst=json_object_get_boolean(v); return 0;
}

int vr_config_parse(vr_config *out, json_object *o, const vr_config *previous, char *err, size_t n)
{
    vr_config c;
    if (previous) c=*previous; else vr_config_default(&c);
    const char *why="invalid configuration";
    if (!o || !json_object_is_type(o,json_type_object)) goto fail;
    json_object_object_foreach(o,key,value) {
        (void)value;
        if (strcmp(key,"enabled") && strcmp(key,"allow_wan") && strcmp(key,"dns_enabled") && strcmp(key,"tls_sni_enabled") &&
            strcmp(key,"port") && strcmp(key,"controller") && strcmp(key,"node_id") &&
            strcmp(key,"node_key") && strcmp(key,"dns_upstream") && strcmp(key,"dns_upstreams") && strcmp(key,"gateway_proxy")) goto fail;
    }
    if (boolean_field(o,"enabled",&c.enabled) || boolean_field(o,"allow_wan",&c.allow_wan) ||
        boolean_field(o,"dns_enabled",&c.dns_enabled) ||
        boolean_field(o,"tls_sni_enabled",&c.tls_sni_enabled) ||
        string_field(o,"controller",c.controller,sizeof(c.controller)) ||
        string_field(o,"node_id",c.node_id,sizeof(c.node_id)) ||
        string_field(o,"dns_upstream",c.dns_upstream,sizeof(c.dns_upstream))) goto fail;
    char secret[sizeof(c.node_key)]={0};
    if (string_field(o,"node_key",secret,sizeof(secret))) goto fail;
    if (*secret) memcpy(c.node_key,secret,sizeof(secret));
    memset(secret,0,sizeof(secret));
    json_object *port=vr_member(o,"port");
    if (port) {
        if (!json_object_is_type(port,json_type_int) || json_object_get_int64(port)<1024 || json_object_get_int64(port)>65535) goto fail;
        c.port=json_object_get_int(port);
    }
    for (const unsigned char *p=(const unsigned char *)c.node_id; *p; p++)
        if (!(isalnum(*p) || *p=='_' || *p=='-' || *p=='.')) goto fail;
    if (*c.controller) {
        char host[254];int port,tls;
        if(vr_origin(c.controller,host,&port,&tls)) {why="controller must be an HTTP(S) IP/hostname origin without credentials or path";goto fail;}
        size_t len=strlen(c.controller); if (len && c.controller[len-1]=='/') c.controller[len-1]=0;
    }
    json_object *upstreams=vr_member(o,"dns_upstreams");
    if(upstreams) {
        if(!json_object_is_type(upstreams,json_type_object) ||
           !vr_member(upstreams,"doh") || !vr_member(upstreams,"dot") || !vr_member(upstreams,"dns") ||
           string_field(upstreams,"doh",c.dns_doh,sizeof(c.dns_doh)) ||
           string_field(upstreams,"dot",c.dns_dot,sizeof(c.dns_dot)) ||
           string_field(upstreams,"dns",c.dns_upstream,sizeof(c.dns_upstream)) ||
           boolean_field(upstreams,"doh_enabled",&c.doh_enabled) ||
           boolean_field(upstreams,"dot_enabled",&c.dot_enabled) ||
           boolean_field(upstreams,"dns_enabled",&c.plain_dns_enabled))goto fail;
        json_object_object_foreach(upstreams,key,value) {
            (void)value;
            if(strcmp(key,"doh")&&strcmp(key,"dot")&&strcmp(key,"dns")&&strcmp(key,"doh_enabled")&&
               strcmp(key,"dot_enabled")&&strcmp(key,"dns_enabled"))goto fail;
        }
    }
    json_object *gateway=vr_member(o,"gateway_proxy");
    if(gateway && vr_gateway_parse(&c.gateway,gateway)){why="invalid gateway config: private CIDRs and allowed nodes required (max 32/64)";goto fail;}
    char host[254];int dns_port;
    if(vr_dns_doh_endpoint(c.dns_doh,host,&dns_port) || vr_dns_endpoint(c.dns_dot,host,&dns_port,0) ||
       vr_dns_endpoint(c.dns_upstream,host,&dns_port,1)) {why="invalid DNS upstreams: require HTTPS DoH, DoT host:port and ordinary IPv4:port";goto fail;}
    if ((c.enabled || c.gateway.enabled) && (!*c.controller || !*c.node_id || !*c.node_key)) {why="controller, node ID and key are required";goto fail;}
    *out=c; if(n)*err=0; return 0;
fail:
    snprintf(err,n,"%s",why); return -1;
}

json_object *vr_config_json(const vr_config *c, int secrets)
{
    json_object *o=json_object_new_object();
    json_object_object_add(o,"gateway_proxy",vr_gateway_json(&c->gateway));
    json_object_object_add(o,"enabled",json_object_new_boolean(c->enabled));
    json_object_object_add(o,"allow_wan",json_object_new_boolean(c->allow_wan));
    json_object_object_add(o,"dns_enabled",json_object_new_boolean(c->dns_enabled));
    json_object_object_add(o,"tls_sni_enabled",json_object_new_boolean(c->tls_sni_enabled));
    json_object_object_add(o,"port",json_object_new_int(c->port));
    json_object_object_add(o,"controller",json_object_new_string(c->controller));
    json_object_object_add(o,"node_id",json_object_new_string(c->node_id));
    json_object *upstreams=json_object_new_object();
    json_object_object_add(upstreams,"doh",json_object_new_string(c->dns_doh));
    json_object_object_add(upstreams,"dot",json_object_new_string(c->dns_dot));
    json_object_object_add(upstreams,"dns",json_object_new_string(c->dns_upstream));
    json_object_object_add(upstreams,"doh_enabled",json_object_new_boolean(c->doh_enabled));
    json_object_object_add(upstreams,"dot_enabled",json_object_new_boolean(c->dot_enabled));
    json_object_object_add(upstreams,"dns_enabled",json_object_new_boolean(c->plain_dns_enabled));
    json_object_object_add(o,"dns_upstreams",upstreams);
    if(secrets) json_object_object_add(o,"node_key",json_object_new_string(c->node_key));
    return o;
}

int vr_config_load(vr_config *c, const char *path, char *err, size_t n)
{
    char buf[16384];size_t len;
    vr_config_default(c);
    if(vr_read_file(path,buf,sizeof(buf),&len)) {
        if(errno==ENOENT)return 0;
        snprintf(err,n,"cannot read configuration");return -1;
    }
    json_object *o=vr_json_parse(buf,len);
    int rc=vr_config_parse(c,o,NULL,err,n);
    memset(buf,0,sizeof(buf));json_object_put(o);return rc;
}

int vr_config_save(const vr_config *c, const char *path)
{
    json_object *o=vr_config_json(c,1);
    const char *s=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);
    char existing[16384];size_t n;
    if(!vr_read_file(path,existing,sizeof(existing),&n)&&n==strlen(s)&&!memcmp(existing,s,n)){memset(existing,0,sizeof(existing));json_object_put(o);return 0;}
    memset(existing,0,sizeof(existing));
    int rc=vr_atomic_write(path,s,strlen(s));json_object_put(o);return rc;
}

int vr_origin(const char *url,char host[254],int *port,int *tls)
{
    const char *p;
    if(!strncmp(url,"https://",8)){p=url+8;*tls=1;*port=443;}
    else if(!strncmp(url,"http://",7)){p=url+7;*tls=0;*port=80;}
    else return -1;
    if(*p=='[') {
        const char *end=strchr(p,']');struct in6_addr ip;
        if(!end||end-p<2||end-p>46)return -1;
        memcpy(host,p+1,(size_t)(end-p-1));host[end-p-1]=0;
        if(inet_pton(AF_INET6,host,&ip)!=1)return -1;
        p=end+1;
    } else {
    size_t len=strcspn(p,":/");
    if(!len || len>253 || p[0]=='-' || p[len-1]=='-')return -1;
    for(size_t i=0;i<len;i++)if(!((p[i]>='a'&&p[i]<='z')||(p[i]>='A'&&p[i]<='Z')||(p[i]>='0'&&p[i]<='9')||p[i]=='.'||p[i]=='-'))return -1;
    memcpy(host,p,len);host[len]=0;p+=len;
    }
    if(*p==':'){
        p++;if(*p<'0'||*p>'9')return -1;
        char *end;long v=strtol(p,&end,10);if(v<1||v>65535)return -1;*port=(int)v;p=end;
    }
    return (!*p || (*p=='/'&&!p[1]))?0:-1;
}
