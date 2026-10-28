#include "service.h"
#include <arpa/inet.h>
#include <errno.h>
#include <stdio.h>
#include <string.h>

int vr_ip_parse(const char *text,vr_ip *ip)
{
    memset(ip,0,sizeof(*ip));
    if(inet_pton(AF_INET,text,ip->bytes)==1){ip->family=4;return 0;}
    if(inet_pton(AF_INET6,text,ip->bytes)!=1)return -1;
    static const unsigned char mapped[12]={0,0,0,0,0,0,0,0,0,0,255,255};
    if(!memcmp(ip->bytes,mapped,12)){memmove(ip->bytes,ip->bytes+12,4);memset(ip->bytes+4,0,12);ip->family=4;}
    else ip->family=6;
    return 0;
}

const char *vr_ip_text(const vr_ip *ip,char out[46])
{return inet_ntop(ip->family==4?AF_INET:AF_INET6,ip->bytes,out,46);}

static int same(const vr_ip *a,const vr_ip *b)
{return a->family==b->family&&!memcmp(a->bytes,b->bytes,16);}

int vr_service_ip_blocked(vr_service *s,const vr_ip *ip,uint64_t now)
{
    (void)now;
    int blocked=0;pthread_mutex_lock(&s->mutex);
    for(size_t i=0;i<s->config.ip_blacklist.count;i++)if(same(ip,&s->config.ip_blacklist.ips[i]))blocked=1;
    for(size_t i=0;i<VR_AUTH_IP_MAX;i++)if(same(ip,&s->auth_ips[i].ip)&&s->auth_ips[i].blocked)blocked=1;
    pthread_mutex_unlock(&s->mutex);return blocked;
}

void vr_service_ip_result(vr_service *s,const vr_ip *ip,int success,uint64_t now)
{
    vr_auth_ip *entry=NULL,*available=NULL;int newly_blocked=0;uint32_t second=(uint32_t)(now/1000);
    pthread_mutex_lock(&s->mutex);
    for(size_t i=0;i<VR_AUTH_IP_MAX;i++) {
        vr_auth_ip *p=&s->auth_ips[i];
        if(same(ip,&p->ip)){entry=p;break;}
        unsigned last=(p->next+VR_AUTH_IP_FAILURES-1)%VR_AUTH_IP_FAILURES;
        if(!available&&(!p->ip.family||(!p->blocked&&(uint32_t)(second-p->failed_at[last])>VR_AUTH_IP_WINDOW_MS/1000)))available=p;
    }
    if(success){if(entry&&!entry->blocked)memset(entry,0,sizeof(*entry));pthread_mutex_unlock(&s->mutex);return;}
    if(!entry&&available){entry=available;memset(entry,0,sizeof(*entry));entry->ip=*ip;}
    if(entry&&!entry->blocked) {
        entry->failed_at[entry->next]=second;entry->next=(entry->next+1)%VR_AUTH_IP_FAILURES;
        if(entry->count<VR_AUTH_IP_FAILURES)entry->count++;
        if(entry->count==VR_AUTH_IP_FAILURES&&(uint32_t)(second-entry->failed_at[entry->next])<=VR_AUTH_IP_WINDOW_MS/1000) {
            entry->blocked=1;newly_blocked=1;s->auth_ips_revision++;if(!s->auth_ips_flush_at)s->auth_ips_flush_at=now+5000;
        }
    }
    pthread_mutex_unlock(&s->mutex);
    if(newly_blocked){char text[46],event[160];vr_ip_text(ip,text);snprintf(event,sizeof(event),"IP %s permanently blocked after 5 authentication failures within 60 minutes",text);vr_service_log(s,"link",event);}
}

void vr_service_ip_unblock(vr_service *s,const vr_ip *ip)
{
    pthread_mutex_lock(&s->mutex);
    for(size_t i=0;i<VR_AUTH_IP_MAX;i++)if(same(ip,&s->auth_ips[i].ip)) {
        if(s->auth_ips[i].blocked){s->auth_ips_revision++;s->auth_ips_flush_at=0;}
        memset(&s->auth_ips[i],0,sizeof(s->auth_ips[i]));
    }
    pthread_mutex_unlock(&s->mutex);
}

json_object *vr_service_ip_status(vr_service *s,uint64_t now)
{
    (void)now;
    json_object *o=json_object_new_object(),*rows=json_object_new_array();pthread_mutex_lock(&s->mutex);
    for(size_t i=0;i<VR_AUTH_IP_MAX;i++) {
        vr_auth_ip *p=&s->auth_ips[i];if(!p->ip.family||!p->blocked)continue;
        char ip[46];vr_ip_text(&p->ip,ip);json_object *row=json_object_new_object();
        json_object_object_add(row,"ip",json_object_new_string(ip));
        json_object_object_add(row,"permanent",json_object_new_boolean(1));
        json_object_array_add(rows,row);
    }
    pthread_mutex_unlock(&s->mutex);json_object_object_add(o,"automatic",rows);return o;
}

static int blacklist_path(vr_service *s,char path[600])
{
    if(vr_copy(path,600,s->config_path))return -1;
    char *slash=strrchr(path,'/');if(!slash)return -1;
    strcpy(slash+1,"ip-blacklist.json");return 0;
}

int vr_service_ip_load(vr_service *s)
{
    char path[600],buffer[4096];size_t n;if(blacklist_path(s,path))return -1;
    if(vr_read_file(path,buffer,sizeof(buffer),&n))return errno==ENOENT?0:-1;
    json_object *list=vr_json_parse(buffer,n);int rc=-1;
    if(!list||!json_object_is_type(list,json_type_array)||json_object_array_length(list)>VR_AUTH_IP_MAX)goto done;
    vr_auth_ip entries[VR_AUTH_IP_MAX]={0};size_t count=0;
    for(size_t i=0;i<(size_t)json_object_array_length(list);i++) {
        json_object *item=json_object_array_get_idx(list,i);vr_ip ip;
        if(!json_object_is_type(item,json_type_string)||strlen(json_object_get_string(item))!=(size_t)json_object_get_string_len(item)||vr_ip_parse(json_object_get_string(item),&ip))goto done;
        int found=0;for(size_t j=0;j<count;j++)if(same(&entries[j].ip,&ip))found=1;
        if(!found){entries[count].ip=ip;entries[count++].blocked=1;}
    }
    pthread_mutex_lock(&s->mutex);memcpy(s->auth_ips,entries,sizeof(entries));pthread_mutex_unlock(&s->mutex);rc=0;
done:json_object_put(list);return rc;
}

int vr_service_ip_flush(vr_service *s,uint64_t now,int force)
{
    char path[600];if(blacklist_path(s,path))return -1;
    pthread_mutex_lock(&s->mutex);
    if(s->auth_ips_revision==s->auth_ips_saved||(!force&&now<s->auth_ips_flush_at)){pthread_mutex_unlock(&s->mutex);return 0;}
    unsigned revision=s->auth_ips_revision;json_object *list=json_object_new_array();
    for(size_t i=0;i<VR_AUTH_IP_MAX;i++)if(s->auth_ips[i].blocked){char ip[46];vr_ip_text(&s->auth_ips[i].ip,ip);json_object_array_add(list,json_object_new_string(ip));}
    pthread_mutex_unlock(&s->mutex);
    const char *encoded=json_object_to_json_string_ext(list,JSON_C_TO_STRING_PLAIN);int rc=vr_atomic_write(path,encoded,strlen(encoded));json_object_put(list);
    pthread_mutex_lock(&s->mutex);
    if(!rc)s->auth_ips_saved=revision;
    s->auth_ips_flush_at=rc?now+60000:(s->auth_ips_revision==revision?0:now+5000);
    pthread_mutex_unlock(&s->mutex);return rc;
}
