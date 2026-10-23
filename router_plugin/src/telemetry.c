#define _GNU_SOURCE
#include "engine.h"
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <netdb.h>

static void str(json_object *o,const char *key,const char *s)
{json_object_object_add(o,key,json_object_new_string(s));}
static void num(json_object *o,const char *key,uint64_t n)
{json_object_object_add(o,key,json_object_new_int64((int64_t)n));}
static void stamp(char out[40])
{
    struct timespec ts;clock_gettime(CLOCK_REALTIME,&ts);struct tm tm;gmtime_r(&ts.tv_sec,&tm);
    char date[24];strftime(date,sizeof(date),"%Y-%m-%dT%H:%M:%S",&tm);
    snprintf(out,40,"%s.%03ldZ",date,ts.tv_nsec/1000000);
}
static void buffer(json_object *items,vr_engine *e,vr_link *l,const char *stage,
                   size_t depth,size_t allocated,size_t cap,size_t peak,size_t peak_alloc)
{
    json_object *o=json_object_new_object();char id[128];
    snprintf(id,sizeof(id),"router:%s:%d:%s",l->route,l->slot,stage);str(o,"id",id);str(o,"stage",stage);
    str(o,"route_id",l->route);str(o,"direction",!strcmp(stage,"carrier_tx_bytes")?"tx":"rx");
    num(o,"carrier_slot",(unsigned)l->slot);num(o,"depth",depth);num(o,"allocated_capacity",allocated);num(o,"max_capacity",cap);
    num(o,"peak_depth",peak);num(o,"peak_allocated_capacity",peak_alloc);str(o,"stats_started_at",e->stats_started_at);
    if(!strcmp(stage,"carrier_tx_bytes")){num(o,"enqueued",l->tx_enqueued);num(o,"dequeued",l->tx_dequeued);num(o,"full_events",l->tx_full);}
    json_object_array_add(items,o);
}
static json_object *state(vr_link *base,int connected,const char *endpoint,const char *now)
{
    json_object *o=json_object_new_object();str(o,"endpoint",endpoint);str(o,"updated_at",now);
    if(connected)str(o,"selected_protocol","websocket");
    else if(*base->error) {
        json_object *qualities=json_object_new_array(),*q=json_object_new_object();
        str(q,"protocol","websocket");str(q,"last_error",base->error);json_object_array_add(qualities,q);json_object_object_add(o,"protocol_qualities",qualities);
    }
    return o;
}

/* Main thread owns links and decoder/queue counters. Never serialize a live
 * policy or carrier from the control thread; copy the immutable snapshot. */
void vr_telemetry_publish(vr_engine *e)
{
    char now[40];stamp(now);
    pthread_mutex_lock(&e->mutex);int reset=e->buffers_reset;e->buffers_reset=0;pthread_mutex_unlock(&e->mutex);
    if(reset||!*e->stats_started_at)vr_copy(e->stats_started_at,sizeof(e->stats_started_at),now);
    json_object *root=json_object_new_object(),*routes=json_object_new_array(),*buf=json_object_new_object(),*items=json_object_new_array();
    str(root,"timestamp",now);str(buf,"stats_started_at",e->stats_started_at);str(buf,"collected_at",now);
    for(size_t i=0;i<e->link_count;i++) {
        vr_link *base=&e->links[i];if(!base->rule||base->slot)continue;
        const char *id=base->route;int outbound=!strcmp(e->config.node_id,vr_string(base->rule,"from_node_id"));
        const char *host=vr_string(base->rule,outbound?"to_service_domain":"from_service_domain");
        int port=json_object_get_int(vr_member(base->rule,outbound?"to_service_port":"from_service_port"));
        char endpoint[300];snprintf(endpoint,sizeof(endpoint),"%s:%d",host,port);
        json_object *row=json_object_new_object(),*sessions=json_object_new_array(),*bridge=json_object_new_object(),*stats=json_object_new_object();int active=0,upstream=0,downstream=0;
        uint64_t opens=0,sent=0,received=0,sent_bytes=0,received_bytes=0;
        for(size_t j=i;j<e->link_count&&j<i+VR_CARRIER_SLOTS;j++) {
            vr_link *l=&e->links[j];if(!l->rule||l->slot>=vr_carrier_count(base->rule))continue;
            if(reset){l->tx_peak=l->used;l->tx_memory_peak=l->tx_memory;l->decoder.peak=l->rx_peak=l->decoder.used;l->tx_enqueued=l->tx_dequeued=l->tx_full=0;}
            if(l->decoder.peak>l->rx_peak)l->rx_peak=l->decoder.peak;
            buffer(items,e,l,"carrier_tx_bytes",l->used,l->tx_memory,VR_TX_CAP,l->tx_peak,l->tx_memory_peak);
            buffer(items,e,l,"carrier_rx_bytes",l->decoder.used,sizeof(l->decoder.bytes),sizeof(l->decoder.bytes),l->rx_peak,sizeof(l->decoder.bytes));
            opens+=l->opens;sent+=l->frames_sent;received+=l->frames_received;sent_bytes+=l->frame_bytes_sent;received_bytes+=l->frame_bytes_received;
            if(!l->ssl)continue;
            active++;if(l->client)upstream++;else downstream++;
            json_object *s=json_object_new_object();char session[120],remote[300];
            snprintf(session,sizeof(session),"router-%s-%d-%llu",l->route,l->slot,(unsigned long long)l->session);
            vr_copy(remote,sizeof(remote),endpoint);
            struct sockaddr_storage addr;socklen_t len=sizeof(addr);char h[NI_MAXHOST],p[NI_MAXSERV];
            if(l->fd>=0&&!getpeername(l->fd,(struct sockaddr *)&addr,&len)&&!getnameinfo((struct sockaddr *)&addr,len,h,sizeof(h),p,sizeof(p),NI_NUMERICHOST|NI_NUMERICSERV))
                snprintf(remote,sizeof(remote),"%.254s:%.12s",h,p);
            str(s,"route_id",id);str(s,"runtime_role","virtual_router");str(s,"direction",l->client?"upstream":"downstream");
            str(s,"bridge_role",l->client?"upstream":"downstream");str(s,"session_id",session);str(s,"remote_addr",remote);
            num(s,"connected_ms",l->connected_at?vr_millis()-l->connected_at:0);num(s,"rtt_ms",l->rtt);
            num(s,"streams_current",1);json_object_array_add(sessions,s);
        }
        str(row,"route_id",id);str(row,"route_name",vr_string(base->rule,"name"));str(row,"route_type","virtual_router");str(row,"role","virtual_router");
        const char *layer=vr_string(base->rule,"route_layer");str(row,"route_layer",*layer?layer:"auto");str(row,"updated_at",now);
        num(stats,"link_open_count",opens);num(stats,"frames_sent",sent);num(stats,"frame_bytes_sent",sent_bytes);
        num(stats,"frames_received",received);num(stats,"frame_bytes_received",received_bytes);
        json_object_object_add(row,"virtual_router",stats);
        str(row,outbound?"next_host":"prev_host",host);num(row,outbound?"next_port":"prev_port",(unsigned)(port>0?port:0));
        str(row,outbound?"next_node_id":"prev_node_id",base->peer);
        str(row,outbound?"next_route_layer":"prev_route_layer","websocket");
        json_object_object_add(row,outbound?"next_state":"prev_state",state(base,active,endpoint,now));
        if(!outbound) {
            str(row,"listen_host","0.0.0.0");num(row,"listen_port",(unsigned)e->config.port);
            json_object *listen=state(base,0,"",now),*statuses=json_object_new_array(),*s=json_object_new_object();
            str(s,"protocol","websocket");str(s,"status",e->tls?"listening":"failed");json_object_array_add(statuses,s);
            json_object_object_add(listen,"listener_statuses",statuses);json_object_object_add(row,"listen_state",listen);
        }
        num(bridge,"upstream_active",(unsigned)upstream);num(bridge,"downstream_active",(unsigned)downstream);str(bridge,"updated_at",now);
        json_object_object_add(bridge,"sessions",json_object_get(sessions));json_object_object_add(row,"bridge_sessions",sessions);json_object_object_add(row,"bridge_status",bridge);
        json_object_array_add(routes,row);
    }
    json_object_object_add(buf,"items",items);json_object_object_add(root,"virtual_router_buffers",buf);json_object_object_add(root,"relay_status",routes);
    pthread_mutex_lock(&e->mutex);json_object *old=e->telemetry;e->telemetry=root;if(reset)e->telemetry_epoch++;pthread_mutex_unlock(&e->mutex);json_object_put(old);
}
