#define _GNU_SOURCE
#include "engine.h"
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

static void status(vr_engine *e,int ready)
{
    vr_telemetry_publish(e);
    json_object *o=json_object_new_object(),*links=vr_links_status(e),*nodes=json_object_new_array();
    json_object_object_add(o,"enabled",json_object_new_boolean(e->config.enabled));
    json_object_object_add(o,"gateway_enabled",json_object_new_boolean(e->config.gateway.enabled));
    json_object_object_add(o,"ready",json_object_new_boolean(ready));
    pthread_mutex_lock(&e->mutex);int connected=e->controller_connected;
    json_object_object_add(o,"controller_endpoint",json_object_new_string(e->controller_endpoint));
    json_object_object_add(o,"controller_direct",json_object_new_boolean(e->controller_direct));pthread_mutex_unlock(&e->mutex);
    char public4[16],public6[46];pthread_mutex_lock(&e->mutex);
    vr_copy(public4,sizeof(public4),e->public_ipv4);vr_copy(public6,sizeof(public6),e->public_ipv6);pthread_mutex_unlock(&e->mutex);
    if(!*public4)vr_public_ip(e->addresses.wan,AF_INET,public4,sizeof(public4));
    json_object_object_add(o,"public_ipv4",json_object_new_string(public4));
    json_object_object_add(o,"public_ipv6",json_object_new_string(public6));
    json_object_object_add(o,"controller_connected",json_object_new_boolean(connected));
    json_object_object_add(o,"error",json_object_new_string(e->error));
    json_object_object_add(o,"lan",json_object_new_string(e->addresses.lan));json_object_object_add(o,"wan",json_object_new_string(e->addresses.wan));
    json_object_object_add(o,"virtual_ip",json_object_new_string(e->own_ip));json_object_object_add(o,"node_id",json_object_new_string(e->config.node_id));
    json_object_object_add(o,"listen_port",json_object_new_int(e->config.port));
    json_object_object_add(o,"route_count",json_object_new_int(e->policy?json_object_array_length(vr_member(e->policy,"route_rules")):0));
    int count=0;pthread_mutex_lock(&e->mutex);for(size_t i=0;i<VR_MAPPING_MAX;i++)if(e->mappings[i].expires>time(NULL))count++;pthread_mutex_unlock(&e->mutex);
    json_object_object_add(o,"mappings",json_object_new_int(count));
    json_object *list=vr_member(e->policy,"probe_ips");
    if(list)for(size_t i=0;i<(size_t)json_object_array_length(list);i++) {
        json_object *src=json_object_array_get_idx(list,i),*v=json_object_new_object();
        json_object_object_add(v,"node_id",json_object_new_string(vr_string(src,"node_id")));
        json_object_object_add(v,"display_name",json_object_new_string(vr_string(src,"display_name")));json_object_array_add(nodes,v);
    }
    json_object *rules=json_object_new_array(),*source=vr_member(e->policy,"route_rules");
    for(size_t i=0;source&&i<(size_t)json_object_array_length(source)&&i<VR_ROUTE_MAX;i++) {
        json_object *r=json_object_array_get_idx(source,i),*v=json_object_new_object();
        const char *fields[]={"id","name","action","exit_node_id","backup_exit_node_id"};
        for(size_t j=0;j<sizeof(fields)/sizeof(fields[0]);j++)json_object_object_add(v,fields[j],json_object_get(vr_member(r,fields[j])));
        json_object_array_add(rules,v);
    }
    json_object_object_add(o,"route_rules",rules);
    json_object_object_add(o,"links",links);json_object_object_add(o,"nodes",nodes);vr_service_status(e->service,o,e->generation);
}

void vr_link_frame(vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{
    if(f->main==1)vr_incoming(l->engine,f,path,n);
    else if(f->main==8)vr_tcp_frame(l->engine,f,path,n);
    else if(f->main>=2&&f->main<=4)vr_measure_frame(l->engine,l,f,path,n);
}

static int cache_path(vr_engine *e,const char *name,char out[512])
{
    if(vr_copy(out,512,e->service->config_path))return -1;
    char *slash=strrchr(out,'/');if(!slash||strlen(name)+(size_t)(slash-out)+2>512)return -1;
    strcpy(slash+1,name);return 0;
}
static SSL_CTX *certificate_context(json_object *cert,const char *node_id)
{
    if(!cert||strcmp(vr_string(cert,"node_id"),node_id))return NULL;
    SSL_CTX *ctx=vr_tls_server_context(vr_string(cert,"cert_pem"),vr_string(cert,"key_pem"));
    if(!ctx)return NULL;
    X509 *leaf=SSL_CTX_get0_certificate(ctx);time_t now=time(NULL),minimum=now+300;
    if(!leaf||X509_cmp_time(X509_get0_notBefore(leaf),&now)>=0||X509_cmp_time(X509_get0_notAfter(leaf),&minimum)<=0) {
        SSL_CTX_free(ctx);return NULL;
    }
    return ctx;
}
static int certificate_cancelled(void *arg)
{vr_engine *e=arg;return vr_service_stopped(e->service,e->generation);}
static int prepare(vr_engine *e)
{
    if(vr_addresses_get(&e->addresses)){strcpy(e->error,"LAN/WAN discovery failed");return -1;}
    char upstream[64];strcpy(upstream,e->config.dns_upstream);*strchr(upstream,':')=0;
    if(!strcmp(upstream,e->addresses.lan)||!strcmp(upstream,e->addresses.wan)){strcpy(e->error,"DNS upstream cannot be this router");return -1;}
    char policy_cache[512],cert_cache[512];
    if(cache_path(e,"route-cache.json",policy_cache)||cache_path(e,"node-certificate.json",cert_cache))return -1;
    vr_service *owner=e->service;
    pthread_mutex_lock(&owner->mutex);
    if(owner->pending_generation==e->generation){e->snapshot=owner->pending_policy;owner->pending_policy=NULL;}
    pthread_mutex_unlock(&owner->mutex);
    e->snapshot=vr_policy_fetch_pending(&e->config,e->addresses.wan_if,policy_cache,e->snapshot,e->error,sizeof(e->error));
    if(!e->snapshot)return -1;
    *e->error=0;
    json_object *dns=vr_member(e->snapshot,"dns_upstreams");
    if(dns) {
        json_object *input=json_object_new_object();json_object_object_add(input,"dns_upstreams",json_object_get(dns));
        vr_service *s=e->service;pthread_mutex_lock(&s->mutex);vr_config next;
        int invalid=s->generation!=e->generation || vr_config_parse(&next,input,&s->config,e->error,sizeof(e->error));
        char target[64]={0};if(!invalid)strcpy(target,next.dns_upstream);char *colon=strchr(target,':');if(colon)*colon=0;
        if(!invalid&&(!strcmp(target,e->addresses.lan)||!strcmp(target,e->addresses.wan)))invalid=1;
        if(!invalid && (strcmp(next.dns_doh,s->config.dns_doh)||strcmp(next.dns_dot,s->config.dns_dot)||strcmp(next.dns_upstream,s->config.dns_upstream))) {
            invalid=vr_config_save(&next,s->config_path);
            if(!invalid)s->config=next;
        }
        if(!invalid)e->config=next;
        pthread_mutex_unlock(&s->mutex);json_object_put(input);
        if(invalid){strcpy(e->error,"cannot apply controller DNS upstreams");return -1;}
    }
    strcpy(upstream,e->config.dns_upstream);*strchr(upstream,':')=0;
    if(!strcmp(upstream,e->addresses.lan)||!strcmp(upstream,e->addresses.wan)){strcpy(e->error,"DNS upstream cannot be this router");return -1;}
    e->policy=vr_member(e->snapshot,"virtual_router");
    e->config.port=vr_policy_service_port(e->policy,e->config.node_id,0);
    char event[240];snprintf(event,sizeof(event),"Listening port from controller: %d",e->config.port);vr_service_log(e->service,"service",event);
    if(!json_object_get_boolean(vr_member(e->policy,"enabled"))){strcpy(e->error,"virtual routing is disabled on controller");return -1;}
    if(vr_copy(e->cidr,sizeof(e->cidr),vr_string(e->policy,"fake_ip_cidr")))return -1;
    if(cache_path(e,"fake-ip-cache.json",e->mapping_path))return -1;
    vr_mapping_load(e);
    json_object *nodes=vr_member(e->policy,"probe_ips");
    for(size_t i=0;i<(size_t)json_object_array_length(nodes);i++) {
        json_object *node=json_object_array_get_idx(nodes,i);
        if(!strcmp(vr_string(node,"node_id"),e->config.node_id))vr_copy(e->own_ip,sizeof(e->own_ip),vr_string(node,"ip"));
    }
    json_object *cached=vr_cache_load(cert_cache);SSL_CTX *fallback=certificate_context(cached,e->config.node_id);json_object_put(cached);
    strcpy(e->error,"Requesting node TLS certificate from controller");status(e,0);
    vr_service_log(e->service,"service",e->error);
    json_object *cert=vr_controller_certificate(&e->config,e->addresses.wan_if,fallback!=NULL,certificate_cancelled,e,e->error,sizeof(e->error));
    if(cert) {
        e->tls=certificate_context(cert,e->config.node_id);
        if(!e->tls)strcpy(e->error,"controller node TLS certificate invalid or expires within 5 minutes");
        else if(vr_cache_save(cert_cache,cert)) {
            SSL_CTX_free(e->tls);e->tls=NULL;strcpy(e->error,"cannot persist node TLS certificate");
        }
    } else {e->tls=fallback;fallback=NULL;}
    SSL_CTX_free(fallback);json_object_put(cert);
    if(!e->tls){if(!*e->error)strcpy(e->error,"node TLS certificate unavailable");return -1;}
    *e->error=0; /* A valid cached certificate recovered a transient HTTP error. */
    json_object *links=vr_member(e->policy,"topology_rules");
    for(size_t i=0;i<(size_t)json_object_array_length(links);i++) {
        json_object *r=json_object_array_get_idx(links,i);if(!json_object_get_boolean(vr_member(r,"enabled")))continue;
        const char *from=vr_string(r,"from_node_id"),*to=vr_string(r,"to_node_id");if(strcmp(from,e->config.node_id)&&strcmp(to,e->config.node_id))continue;
        if(vr_links_add(e,r))return -1;
    }
    if(vr_service_stopped(e->service,e->generation))return -1;
    e->lan=vr_bind4(e->addresses.lan,e->config.port,1);
    if(e->config.allow_wan&&*e->addresses.wan)e->wan=vr_bind4(e->addresses.wan,e->config.port,1);
    if(e->lan<0||(e->config.allow_wan&&*e->addresses.wan&&e->wan<0)){strcpy(e->error,"carrier address or port unavailable");return -1;}
    e->tun=vr_platform_start(&e->config,&e->addresses,e->own_ip,e->cidr);
    if(e->tun<0){strcpy(e->error,"TUN/routing/firewall initialization failed");return -1;}e->platform_started=1;
    if(e->config.enabled&&e->config.tls_sni_enabled&&vr_tcp_start(e)){strcpy(e->error,"TLS sniff listener unavailable");return -1;}
    if(vr_platform_routes(e->policy)){strcpy(e->error,"published route initialization failed");return -1;}
    if(vr_dataplane_start(e)){strcpy(e->error,"mapping resolver initialization failed");return -1;}
    if(e->config.enabled&&e->config.dns_enabled&&vr_dns_start(e)){strcpy(e->error,"DNS initialization failed");return -1;}
    vr_telemetry_publish(e);
    if(vr_control_start(e)){strcpy(e->error,"controller worker initialization failed");return -1;}
    return 0;
}

void vr_engine_run(vr_service *service,const vr_config *config,unsigned generation)
{
    vr_engine *e=calloc(1,sizeof(*e));if(!e)return;
    e->service=service;e->config=*config;e->generation=generation;e->tun=e->lan=e->wan=e->dns_udp=e->dns_tcp=e->carrier6=-1;
    pthread_mutex_init(&e->mutex,NULL);pthread_cond_init(&e->jobs_changed,NULL);
    for(size_t i=0;i<VR_CARRIER_MAX;i++)e->links[i].fd=-1;
    if(prepare(e))goto done;
    uint64_t published=0,addresses_checked=vr_millis(),sniff_retry=0,firewall_retry=0,carrier6_checked=0;
    unsigned firewall_failures=0;
    vr_address_watch address_watch={0};
    while(!vr_service_stopped(service,generation)) {
        uint64_t now=vr_millis();
        if(now>=carrier6_checked) {
            int listener=e->carrier6>=0?e->carrier6:vr_bind6_carrier(e->config.port);
            if(listener>=0) {
                if(!vr_platform_carrier6(&e->config,&e->addresses)) {
                    if(e->carrier6<0)vr_service_log(service,"link","IPv6 carrier listener ready");
                    e->carrier6=listener;
                } else if(e->carrier6<0){close(listener);vr_service_log(service,"link","IPv6 carrier listener deferred; IPv4 retained");}
            }
            carrier6_checked=now+30000;
        }
        pthread_mutex_lock(&service->mutex);int sniff=service->config.tls_sni_enabled;vr_exit_overrides exits=service->config.exit_overrides;pthread_mutex_unlock(&service->mutex);
        pthread_mutex_lock(&e->mutex);e->config.exit_overrides=exits;pthread_mutex_unlock(&e->mutex);
        if(sniff!=e->config.tls_sni_enabled&&now>=sniff_retry) {
            if(!e->config.enabled||(!(sniff&&vr_tcp_start(e))&&!vr_platform_tls(&e->addresses,e->cidr,sniff))) {
                e->config.tls_sni_enabled=sniff;vr_service_log(service,"tls",sniff?"SNI enabled for new LAN connections":"SNI disabled for new connections; active streams retained");
            } else vr_service_log(service,"tls","SNI switch failed; current connections retained, retrying");
            sniff_retry=now+5000;
        }
        vr_handshakes_poll(e,0);
        if(vr_policy_apply_pending(e)==2){strcpy(e->error,"controller interface configuration changed; reloading");break;}
        pthread_mutex_lock(&e->mutex);int pending_policy=e->next_policy!=NULL;pthread_mutex_unlock(&e->mutex);
        vr_dataplane_tick(e);
        for(size_t i=0;i<e->link_count;i++) {
            vr_link *l=&e->links[i];
            if(l->ssl && (l->auth_expires<=time(NULL)||now-l->last_rx>90000))vr_link_close(l,"link authorization or idle timeout");
            if(l->ssl&&now-l->keepalive_at>=20000) {
                if(!vr_link_queue(l,9,"",0))l->keepalive_at=now;
            }
            if(!l->ssl&&!pending_policy)vr_link_dial(l);
        }
        struct pollfd fds[6+VR_CARRIER_MAX+129];fds[0]=(struct pollfd){e->tun,POLLIN,0};fds[1]=(struct pollfd){e->lan,POLLIN,0};
        fds[2]=(struct pollfd){e->wan,POLLIN,0};fds[3]=(struct pollfd){e->dns_udp,POLLIN,0};fds[4]=(struct pollfd){e->dns_tcp,POLLIN,0};
        fds[5]=(struct pollfd){e->carrier6,POLLIN,0};
        for(size_t i=0;i<e->link_count;i++)fds[6+i]=(struct pollfd){e->links[i].fd,(short)(POLLIN|(e->links[i].used?(e->links[i].write_events?e->links[i].write_events:POLLOUT):0)),0};
        int pending=0;for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl&&SSL_pending(e->links[i].ssl))pending=1;
        size_t tcp_count=vr_tcp_pollfds(e,fds+6+e->link_count);
        int result=poll(fds,6+e->link_count+tcp_count,pending?0:50);if(result<0&&errno==EINTR)continue;if(result<0){strcpy(e->error,"network poll failed");break;}
        for(size_t i=0;i<e->link_count;i++)if(e->links[i].ssl)vr_link_poll(&e->links[i],fds[6+i].revents);
        if(fds[0].revents&POLLIN){uint8_t packet[2048];ssize_t n=read(e->tun,packet,sizeof(packet));if(n>0)vr_outgoing(e,packet,(size_t)n);}
        for(size_t i=1;i<=2;i++)if(!pending_policy&&(fds[i].revents&POLLIN)){int fd=accept4(fds[i].fd,NULL,NULL,SOCK_CLOEXEC);if(fd>=0)vr_link_accept(e,fd);}
        if(!pending_policy&&(fds[5].revents&POLLIN)){int fd=accept4(fds[5].fd,NULL,NULL,SOCK_CLOEXEC);if(fd>=0)vr_link_accept(e,fd);}
        if(fds[3].revents&POLLIN)vr_dns_accept(e,0);
        if(fds[4].revents&POLLIN)vr_dns_accept(e,1);
        if(tcp_count)vr_tcp_poll(e,fds+6+e->link_count);
        vr_measure_tick(e);
        if(now-published>=1000){status(e,1);published=now;}
        if(now-addresses_checked>=10000) {
            vr_addresses next;char detail[240];
            int change=vr_addresses_check(&address_watch,&e->addresses,&next,detail,sizeof(detail));
            if(change)vr_service_log(service,"service",detail);
            if(change==2) {
                if(vr_addresses_apply(e,&next))vr_service_log(service,"service","Address refresh deferred; existing connections retained, retrying");
                else {address_watch.pending=0;vr_service_log(service,"service","LAN/WAN listeners and rules updated; existing connections retained");}
            }
            if(!vr_platform_check(detail,sizeof(detail))) {
                vr_service_log(service,"service",detail);
                if(firewall_failures<2)firewall_failures++;
                if(firewall_failures>=2&&vr_millis()>=firewall_retry) {
                    if(vr_platform_repair(&e->config,&e->addresses,e->cidr))
                        vr_service_log(service,"service","Firewall repair deferred; existing connections retained, retrying");
                    else {firewall_failures=0;vr_service_log(service,"service","Owned firewall rules reconciled; existing connections retained");}
                    firewall_retry=vr_millis()+30000;
                }
            } else firewall_failures=0;
            if(e->config.enabled&&e->config.dns_enabled&&vr_platform_dns(1))
                vr_service_log(service,"service","DNS upstream reconciliation deferred; existing connections retained, retrying");
            addresses_checked=vr_millis();
        }
    }
done:
    /* Restore ordinary DNS before waiting for controller network deadlines. */
    if(e->platform_started)vr_platform_dns(0);
    vr_control_stop(e);
    vr_measure_stop(e);
    status(e,0);
    if(*e->error)vr_service_log(e->service,"service",e->error);
    /* Restore dnsmasq while worker sockets are still open, then join bounded jobs. */
    vr_dns_stop(e);
    vr_dataplane_stop(e);
    vr_tcp_stop(e);
    vr_handshakes_poll(e,1);
    for(size_t i=0;i<e->link_count;i++)vr_link_close(&e->links[i],"");
    if(e->lan>=0)close(e->lan);
    if(e->wan>=0)close(e->wan);
    if(e->carrier6>=0)close(e->carrier6);
    vr_platform_carrier6_stop();
    if(e->platform_started)vr_platform_stop(e->cidr);
    if(e->tun>=0)close(e->tun);
    for(size_t i=0;i<4;i++)json_object_put(e->receives[i].message);
    json_object_put(e->telemetry);json_object_put(e->speed_send);json_object_put(e->snapshot);json_object_put(e->next_policy);SSL_CTX_free(e->tls);
    pthread_cond_destroy(&e->jobs_changed);pthread_mutex_destroy(&e->mutex);OPENSSL_cleanse(&e->config,sizeof(e->config));free(e);
}
