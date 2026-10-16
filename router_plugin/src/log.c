#include "service.h"
#include <stdio.h>
#include <string.h>

void vr_service_log(vr_service *s,const char *component,const char *message)
{
    if(!message || !*message)return;
    char text[240],source[16];snprintf(text,sizeof(text),"%s",message);snprintf(source,sizeof(source),"%s",component);
    for(size_t i=0;text[i];i++)if((unsigned char)text[i]<32)text[i]=' ';
    pthread_mutex_lock(&s->mutex);
    if(s->log_count) {
        vr_log_entry *last=&s->logs[(s->log_head+VR_LOG_MAX-1)%VR_LOG_MAX];
        if(!strcmp(last->component,source)&&!strcmp(last->message,text)) {
            if(last->repeats<1000000)last->repeats++;
            last->at=time(NULL);pthread_mutex_unlock(&s->mutex);return;
        }
    }
    vr_log_entry *entry=&s->logs[s->log_head];entry->at=time(NULL);entry->repeats=1;
    strcpy(entry->component,source);strcpy(entry->message,text);
    s->log_head=(s->log_head+1)%VR_LOG_MAX;if(s->log_count<VR_LOG_MAX)s->log_count++;
    pthread_mutex_unlock(&s->mutex);
    fprintf(stderr,"virtual router [%s]: %s\n",source,text);
}

json_object *vr_service_logs(vr_service *s)
{
    json_object *o=json_object_new_object(),*items=json_object_new_array();
    pthread_mutex_lock(&s->mutex);
    for(size_t i=0;i<s->log_count;i++) {
        vr_log_entry *entry=&s->logs[(s->log_head+VR_LOG_MAX-s->log_count+i)%VR_LOG_MAX];
        json_object *v=json_object_new_object();
        json_object_object_add(v,"time",json_object_new_int64(entry->at));
        json_object_object_add(v,"component",json_object_new_string(entry->component));
        json_object_object_add(v,"message",json_object_new_string(entry->message));
        json_object_object_add(v,"repeats",json_object_new_int64(entry->repeats));json_object_array_add(items,v);
    }
    pthread_mutex_unlock(&s->mutex);
    json_object_object_add(o,"ok",json_object_new_boolean(1));json_object_object_add(o,"items",items);return o;
}
