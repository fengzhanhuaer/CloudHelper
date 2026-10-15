#include "engine.h"
#include <stdio.h>
#include <string.h>

static void reverse(char path[VR_PATH_MAX][129],size_t n)
{for(size_t i=0;i<n/2;i++){char tmp[129];memcpy(tmp,path[i],129);memcpy(path[i],path[n-i-1],129);memcpy(path[n-i-1],tmp,129);}}

static void message_path(json_object *m,char path[VR_PATH_MAX][129],size_t n)
{json_object *a=json_object_new_array();for(size_t i=0;i<n;i++)json_object_array_add(a,json_object_new_string(path[i]));json_object_object_add(m,"path",a);}

static void complete(vr_engine *e,json_object *result,const char *error)
{
    vr_service *s=e->service;pthread_mutex_lock(&s->mutex);
    json_object *job=json_object_new_object();json_object_object_add(job,"running",json_object_new_boolean(0));
    json_object_object_add(job,"kind",json_object_new_string(vr_string(s->measurement,"kind")));
    if(error)json_object_object_add(job,"error",json_object_new_string(error));
    if(result){const char *text=json_object_to_json_string_ext(result,JSON_C_TO_STRING_PLAIN);json_object_object_add(job,"result",vr_json_parse(text,strlen(text)));}
    json_object_put(s->measurement);s->measurement=job;*s->measure_node=0;pthread_mutex_unlock(&s->mutex);*e->measure_id=0;
}

static int send_start(vr_engine *e,json_object *m,char path[VR_PATH_MAX][129],size_t n)
{
    if(e->speed_send)return -1;
    int64_t bytes=json_object_get_int64(vr_member(m,"max_bytes")),duration=json_object_get_int64(vr_member(m,"max_duration_ms"));
    if(bytes<=0||bytes>16*1024*1024)bytes=16*1024*1024;
    if(duration<=0||duration>3000)duration=3000;
    e->speed_limit=(uint64_t)bytes;e->speed_duration=(uint64_t)duration;
    json_object_object_add(m,"max_bytes",json_object_new_int64(bytes));json_object_object_add(m,"max_duration_ms",json_object_new_int64(duration));message_path(m,path,n);
    if(vr_send_json(e,4,1,path,n,m))return -1;
    e->speed_send=json_object_get(m);memcpy(e->speed_path,path,n*129);e->speed_count=n;e->speed_started=vr_millis();e->speed_bytes=0;return 0;
}

void vr_measure_frame(vr_engine *e,vr_link *l,const vr_frame *f,char path[VR_PATH_MAX][129],size_t n)
{
    if(f->main==4 && f->sub==2) {
        if(f->data_len<6 || memcmp(f->data,"VRS1",4))return;
        size_t len=(size_t)((f->data[4]<<8)|f->data[5]);if(!len||len>128||6+len>f->data_len)return;
        for(size_t i=0;i<4;i++) {
            vr_speed_rx *r=&e->receives[i];
            if(!r->message || strlen(r->id)!=len || memcmp(r->id,f->data+6,len) || r->count!=n || memcmp(r->path,path,n*129))continue;
            if(f->data_len>r->limit-r->bytes || vr_millis()>r->deadline)return;
            if(!r->frames)r->first=vr_millis();
            r->last=vr_millis();r->bytes+=f->data_len;r->frames++;return;
        }
        return;
    }
    json_object *m=vr_json_parse(f->data,f->data_len);const char *id=vr_string(m,"request_id");
    if(!m || !*id || strlen(id)>128){json_object_put(m);return;}
    if(f->main==2 || f->main==3) {
        if(f->sub==1){json_object_object_add(m,"ok",json_object_new_boolean(1));json_object_object_add(m,"responder",json_object_new_string(e->config.node_id));reverse(path,n);vr_send_json(e,f->main,2,path,n,m);}
        else if(f->sub==2) {
            if(!strcmp(id,l->ping_id)) {l->rtt=vr_millis()-l->ping_time;*l->ping_id=0;}
            if(!strcmp(id,e->measure_id)) {json_object_object_add(m,"latency_ms",json_object_new_int64((int64_t)(vr_millis()-e->measure_started)));complete(e,m,NULL);}
        }
    } else if(f->main==4) {
        if(f->sub==1) {
            for(size_t i=0;i<4;i++) {
                vr_speed_rx *r=&e->receives[i];if(r->message && vr_millis()-r->last>10000){json_object_put(r->message);memset(r,0,sizeof(*r));}
                if(!r->message){r->message=json_object_get(m);strcpy(r->id,id);memcpy(r->path,path,n*129);r->count=n;r->last=vr_millis();
                    int64_t limit=json_object_get_int64(vr_member(m,"max_bytes"));r->limit=limit>0&&limit<16*1024*1024?(uint64_t)limit:16*1024*1024;
                    r->deadline=r->last+5000;break;}
            }
        } else if(f->sub==3) {
            for(size_t i=0;i<4;i++) {
                vr_speed_rx *r=&e->receives[i];if(!r->message||strcmp(r->id,id)||r->count!=n||memcmp(r->path,path,n*129))continue;
                uint64_t duration=r->last-r->first;if(!duration||!r->frames)duration=1;
                json_object_object_add(r->message,"bytes",json_object_new_int64((int64_t)r->bytes));json_object_object_add(r->message,"frames",json_object_new_int64((int64_t)r->frames));
                json_object_object_add(r->message,"duration_ms",json_object_new_int64((int64_t)duration));json_object_object_add(r->message,"mbps",json_object_new_double((double)r->bytes*8/(double)duration/1000));
                json_object_object_add(r->message,"ok",json_object_new_boolean(1));json_object_object_add(r->message,"responder",json_object_new_string(e->config.node_id));
                if(!strcmp(vr_string(r->message,"result_node_id"),e->config.node_id)) {if(!strcmp(id,e->measure_id))complete(e,r->message,NULL);}
                else {reverse(path,n);message_path(r->message,path,n);vr_send_json(e,4,4,path,n,r->message);}
                json_object_put(r->message);memset(r,0,sizeof(*r));break;
            }
        } else if(f->sub==4) {
            if(!strcmp(id,e->measure_id)&&!strcmp(vr_string(m,"result_node_id"),e->config.node_id))complete(e,m,NULL);
        } else if(f->sub==5) {
            reverse(path,n);
            if(send_start(e,m,path,n)){json_object_object_add(m,"ok",json_object_new_boolean(0));json_object_object_add(m,"error",json_object_new_string("measurement busy"));vr_send_json(e,4,4,path,n,m);}
        }
    }
    json_object_put(m);
}

void vr_measure_tick(vr_engine *e)
{
    uint64_t now=vr_millis();
    for(size_t i=0;i<e->link_count;i++) {
        vr_link *l=&e->links[i];if(!l->ssl || now-l->ping_time<20000)continue;
        char path[VR_PATH_MAX][129]={{0}};strcpy(path[0],e->config.node_id);strcpy(path[1],l->peer);
        json_object *m=json_object_new_object();vr_random_hex(l->ping_id,24);l->ping_time=now;
        json_object_object_add(m,"request_id",json_object_new_string(l->ping_id));json_object_object_add(m,"source_node_id",json_object_new_string(e->config.node_id));
        json_object_object_add(m,"target_node_id",json_object_new_string(l->peer));message_path(m,path,2);vr_send_json(e,2,1,path,2,m);json_object_put(m);
    }
    vr_service *s=e->service;char node[129]={0},kind[16]={0};
    pthread_mutex_lock(&s->mutex);if(*s->measure_node){strcpy(node,s->measure_node);strcpy(kind,s->measure_kind);*s->measure_node=0;}pthread_mutex_unlock(&s->mutex);
    if(*node) {
        char path[VR_PATH_MAX][129]={{0}};size_t n;
        if(vr_path(e,node,path,&n)||e->speed_send||vr_random_hex(e->measure_id,24)){complete(e,NULL,"target has no available path or measurement busy");}
        else {
            e->measure_started=now;json_object *m=json_object_new_object();
            json_object_object_add(m,"request_id",json_object_new_string(e->measure_id));json_object_object_add(m,"direction",json_object_new_string(kind));
            json_object_object_add(m,"source_node_id",json_object_new_string(e->config.node_id));json_object_object_add(m,"target_node_id",json_object_new_string(node));
            json_object_object_add(m,"result_node_id",json_object_new_string(e->config.node_id));message_path(m,path,n);
            json_object_object_add(m,"max_bytes",json_object_new_int(16*1024*1024));json_object_object_add(m,"max_duration_ms",json_object_new_int(3000));
            int rc=!strcmp(kind,"ping")?vr_send_json(e,3,1,path,n,m):!strcmp(kind,"download")?vr_send_json(e,4,5,path,n,m):send_start(e,m,path,n);
            if(rc)complete(e,NULL,"measurement could not start");
            json_object_put(m);
        }
    }
    /* Starting a measurement may advance the clock. Never subtract its newer
       timestamp from the tick's earlier unsigned timestamp. */
    now=vr_millis();
    if(e->speed_send) {
        if(now-e->speed_started>=e->speed_duration || e->speed_bytes>=e->speed_limit) {
            if(!vr_send_json(e,4,3,e->speed_path,e->speed_count,e->speed_send)){json_object_put(e->speed_send);e->speed_send=NULL;}
            else if(now-e->speed_started>5000){int local=!strcmp(vr_string(e->speed_send,"request_id"),e->measure_id);json_object_put(e->speed_send);e->speed_send=NULL;if(local)complete(e,NULL,"measurement link stalled");}
        } else {
            uint8_t chunk[8192]={0};const char *id=vr_string(e->speed_send,"request_id");size_t len=strlen(id);
            memcpy(chunk,"VRS1",4);chunk[4]=(uint8_t)(len>>8);chunk[5]=(uint8_t)len;memcpy(chunk+6,id,len);
            for(unsigned i=0;i<4 && e->speed_bytes<e->speed_limit;i++){
                size_t size=sizeof(chunk);if(size>e->speed_limit-e->speed_bytes)size=(size_t)(e->speed_limit-e->speed_bytes);
                if(size<6+len){e->speed_bytes=e->speed_limit;break;}
                if(vr_send(e,4,2,e->speed_path,e->speed_count,chunk,size))break;
                e->speed_bytes+=size;}
        }
    }
    if(*e->measure_id && now-e->measure_started>12000)complete(e,NULL,"measurement timed out");
}

void vr_measure_stop(vr_engine *e)
{
    pthread_mutex_lock(&e->service->mutex);int running=json_object_get_boolean(vr_member(e->service->measurement,"running"));pthread_mutex_unlock(&e->service->mutex);
    if(running)complete(e,NULL,"service stopped or configuration changed");
}
