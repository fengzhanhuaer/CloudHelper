#define _GNU_SOURCE
#include "engine.h"
#include <arpa/inet.h>
#include <dirent.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

static int usable(vr_engine *e,const vr_mapping *m,time_t now)
{
    struct in_addr a;
    if(!*m->domain||!*m->exit||m->expires<=now||!vr_cidr_contains(e->cidr,m->ip)||inet_pton(AF_INET,m->ip,&a)!=1)return 0;
    json_object *nodes=vr_member(e->policy,"probe_ips");
    size_t count=nodes&&json_object_is_type(nodes,json_type_array)?json_object_array_length(nodes):0;
    for(size_t i=0;i<count;i++)
        if(!strcmp(m->ip,vr_string(json_object_array_get_idx(nodes,i),"ip")))return 0;
    return 1;
}

static int decode(json_object *o,vr_mapping *m)
{
    memset(m,0,sizeof(*m));
    return vr_copy(m->domain,sizeof(m->domain),vr_string(o,"domain"))||
        vr_copy(m->ip,sizeof(m->ip),vr_string(o,"fake_ip"))||
        vr_copy(m->rule,sizeof(m->rule),vr_string(o,"rule_id"))||
        vr_copy(m->exit,sizeof(m->exit),vr_string(o,"exit_node_id"));
}

static json_object *encode(const vr_mapping *m)
{
    json_object *o=json_object_new_object();
    json_object_object_add(o,"domain",json_object_new_string(m->domain));
    json_object_object_add(o,"fake_ip",json_object_new_string(m->ip));
    json_object_object_add(o,"rule_id",json_object_new_string(m->rule));
    json_object_object_add(o,"exit_node_id",json_object_new_string(m->exit));
    json_object_object_add(o,"expires",json_object_new_int64((int64_t)m->expires));return o;
}

static int scope(vr_engine *e)
{
    json_object *o=json_object_new_array();
    json_object_array_add(o,json_object_new_string(e->config.controller));
    json_object_array_add(o,json_object_new_string(e->config.node_id));
    json_object_array_add(o,json_object_new_string(e->cidr));
    json_object_array_add(o,json_object_get(vr_member(e->policy,"route_rules")));
    int rc=vr_hmac(e->config.node_key,json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN),e->mapping_scope);
    json_object_put(o);return rc;
}

void vr_mapping_reconcile(vr_engine *e)
{
    time_t now=time(NULL);
    for(size_t i=0;i<VR_MAPPING_MAX;i++) {
        vr_mapping *m=&e->mappings[i];if(m->expires<=now)continue;
        char ip[16]="";struct in_addr a={m->resolved};
        if(m->resolved)inet_ntop(AF_INET,&a,ip,sizeof(ip));
        else if(inet_pton(AF_INET,m->domain,&a)==1)strcpy(ip,m->domain);
        vr_decision d=vr_policy_match(e->policy,m->domain,*ip?ip:NULL);
        if(!usable(e,m,now)||d.action!=VR_PROXY||!*d.exit_node||
           (vr_policy_needs_ip(e->policy,m->domain)&&!*ip))memset(m,0,sizeof(*m));
        else {strcpy(m->rule,d.rule);strcpy(m->exit,d.exit_node);}
    }
    (void)scope(e);e->mapping_dirty++;
}

static void remove_interrupted_write(const char *path)
{
    char parent[512];if(vr_copy(parent,sizeof(parent),path))return;
    char *name=strrchr(parent,'/');if(!name)return;*name++=0;
    size_t length=strlen(name);DIR *dir=opendir(*parent?parent:"/");if(!dir)return;
    struct dirent *entry;
    while((entry=readdir(dir))) {
        if(strncmp(entry->d_name,name,length)||strlen(entry->d_name)!=length+7||entry->d_name[length]!='.')continue;
        int valid=1;for(size_t i=length+1;i<length+7;i++) {
            char c=entry->d_name[i];if(!((c>='a'&&c<='z')||(c>='A'&&c<='Z')||(c>='0'&&c<='9')))valid=0;
        }
        if(valid)unlinkat(dirfd(dir),entry->d_name,0);
    }
    closedir(dir);
}

/* Called before workers start: invalid caches are disposable, never policy. */
void vr_mapping_load(vr_engine *e)
{
    if(scope(e)||!*e->mapping_path)return;
    remove_interrupted_write(e->mapping_path);
    struct stat st;
    if(!lstat(e->mapping_path,&st)&&S_ISREG(st.st_mode)&&st.st_size>=(off_t)VR_MAPPING_FILE_MAX){unlink(e->mapping_path);return;}
    char *bytes=malloc(VR_MAPPING_FILE_MAX);size_t n;
    if(!bytes)return;
    json_object *root=vr_read_file(e->mapping_path,bytes,VR_MAPPING_FILE_MAX,&n)?NULL:vr_json_parse(bytes,n);free(bytes);
    json_object *items=vr_member(root,"items");time_t now=time(NULL);
    if(!root||json_object_get_int(vr_member(root,"version"))!=1||strcmp(vr_string(root,"scope"),e->mapping_scope)||
       json_object_get_int64(vr_member(root,"saved_at"))>now||!items||!json_object_is_type(items,json_type_array)||
       json_object_array_length(items)>VR_MAPPING_MAX)goto done;
    size_t slot=0;
    for(size_t i=0;i<(size_t)json_object_array_length(items);i++) {
        json_object *o=json_object_array_get_idx(items,i);vr_mapping m;
        if(decode(o,&m))continue;
        int64_t expires=json_object_get_int64(vr_member(o,"expires"));
        if(expires<=now||expires>(int64_t)now+VR_MAPPING_TTL)continue;
        m.expires=(time_t)expires;if(!usable(e,&m,now))continue;
        /* The snapshot is MRU-first; preserve that order on subsequent saves. */
        m.used=VR_MAPPING_MAX-i;e->mappings[slot++]=m;
    }
    if(slot&&e->service)vr_service_log(e->service,"dns","Restored unexpired fake IP cache including exit nodes");
done:
    json_object_put(root);
}

typedef struct {vr_mapping value;} saved_mapping;
static int recent(const void *a,const void *b)
{
    uint64_t x=((const saved_mapping *)a)->value.used,y=((const saved_mapping *)b)->value.used;
    return x>y?-1:x<y?1:0;
}

int vr_mapping_flush(vr_engine *e,int force)
{
    uint64_t tick=vr_millis();
    pthread_mutex_lock(&e->mutex);
    if(!*e->mapping_path||!*e->mapping_scope){pthread_mutex_unlock(&e->mutex);return 0;}
    if(!e->mapping_dirty||(!force&&tick-e->mapping_flush_at<60000)){pthread_mutex_unlock(&e->mutex);return 0;}
    e->mapping_flush_at=tick;unsigned revision=e->mapping_dirty;size_t count=0;time_t now=time(NULL);
    char saved_scope[65];strcpy(saved_scope,e->mapping_scope);
    saved_mapping *saved=malloc(sizeof(*saved)*VR_MAPPING_MAX);
    if(!saved){pthread_mutex_unlock(&e->mutex);return -1;}
    for(size_t i=0;i<VR_MAPPING_MAX;i++)if(usable(e,&e->mappings[i],now))saved[count++].value=e->mappings[i];
    pthread_mutex_unlock(&e->mutex);qsort(saved,count,sizeof(*saved),recent);
    json_object *root=json_object_new_object(),*items=json_object_new_array();
    json_object_object_add(root,"version",json_object_new_int(1));
    json_object_object_add(root,"scope",json_object_new_string(saved_scope));
    json_object_object_add(root,"saved_at",json_object_new_int64((int64_t)now));
    json_object_object_add(root,"items",items);
    size_t length=strlen(json_object_to_json_string_ext(root,JSON_C_TO_STRING_PLAIN));
    for(size_t i=0;i<count;i++) {
        json_object *item=encode(&saved[i].value);
        size_t extra=strlen(json_object_to_json_string_ext(item,JSON_C_TO_STRING_PLAIN))+(i?1:0);
        if(length+extra>=VR_MAPPING_FILE_MAX){json_object_put(item);break;}
        json_object_array_add(items,item);length+=extra;
    }
    free(saved);
    const char *text=json_object_to_json_string_ext(root,JSON_C_TO_STRING_PLAIN);size_t n=strlen(text);
    int rc=n>=VR_MAPPING_FILE_MAX?-1:vr_atomic_write(e->mapping_path,text,n);json_object_put(root);
    pthread_mutex_lock(&e->mutex);
    if(!rc&&revision==e->mapping_dirty)e->mapping_dirty=0;
    pthread_mutex_unlock(&e->mutex);
    if(rc&&e->service)vr_service_log(e->service,"dns","Cannot persist fake IP cache; keeping memory cache");
    return rc;
}

static int lookup(vr_engine *e,const char *domain,const char *ip,vr_decision d,vr_mapping *out)
{
    time_t now=time(NULL);
    for(size_t i=0;i<VR_MAPPING_MAX;i++) {
        vr_mapping *m=&e->mappings[i];
        if(m->expires>now&&((domain&&*domain&&!strcmp(m->domain,domain)&&!strcmp(m->rule,d.rule)&&!strcmp(m->exit,d.exit_node))||
           (ip&&*ip&&!strcmp(m->ip,ip)))){m->used=vr_millis();*out=*m;return 0;}
    }
    return -1;
}

int vr_mapping_get(vr_engine *e,const char *domain,const char *ip,vr_decision d,vr_mapping *out)
{
    vr_addresses addresses=vr_engine_addresses(e);
    pthread_mutex_lock(&e->mutex);unsigned revision=e->policy_revision;
    int stale=domain&&*domain&&d.revision!=revision;pthread_mutex_unlock(&e->mutex);
    if(stale)return -1;
    if(!vr_mapping_lookup(e,domain,ip,d,out))return 0;
    json_object *body=json_object_new_object();char key[65];
    if(domain&&*domain) {
        json_object_object_add(body,"domain",json_object_new_string(domain));json_object_object_add(body,"rule_id",json_object_new_string(d.rule));
        json_object_object_add(body,"action",json_object_new_string("probe_exit"));json_object_object_add(body,"exit_node_id",json_object_new_string(d.exit_node));
    } else json_object_object_add(body,"fake_ip",json_object_new_string(ip?ip:""));
    if(vr_hmac(e->config.node_key,json_object_to_json_string_ext(body,JSON_C_TO_STRING_PLAIN),key)){json_object_put(body);return -1;}
    uint64_t deadline=vr_millis()+25000;size_t slot=0;
    for(;;) {
        pthread_mutex_lock(&e->mutex);
        if(revision!=e->policy_revision){pthread_mutex_unlock(&e->mutex);json_object_put(body);return -1;}
        if(!lookup(e,domain,ip,d,out)){pthread_mutex_unlock(&e->mutex);json_object_put(body);return 0;}
        size_t match=16,free_slot=16;uint64_t now=vr_millis();
        for(size_t i=0;i<16;i++) {
            vr_mapping_pending *p=&e->mapping_pending[i];
            if(!strcmp(p->key,key)){match=i;break;}
            if(!p->busy&&(free_slot==16||p->used<e->mapping_pending[free_slot].used))free_slot=i;
        }
        if(match<16&&!e->mapping_pending[match].busy&&now<e->mapping_pending[match].retry){pthread_mutex_unlock(&e->mutex);json_object_put(body);return -1;}
        if((match<16&&!e->mapping_pending[match].busy)||(match==16&&free_slot<16)) {
            slot=match<16?match:free_slot;vr_mapping_pending *p=&e->mapping_pending[slot];strcpy(p->key,key);p->busy=1;p->used=now;
            pthread_mutex_unlock(&e->mutex);break;
        }
        pthread_mutex_unlock(&e->mutex);
        if(now>=deadline){json_object_put(body);return -1;}poll(NULL,0,10);
    }
    char error[256]={0};json_object *response=vr_controller_call(&e->config,addresses.wan_if,"POST","/api/probe/route/fake_ip/resolve",body,error,sizeof(error));json_object_put(body);
    json_object *item=vr_member(response,"item");vr_mapping m;int rc=-1;time_t now=time(NULL);
    if(!item||strcmp(vr_string(item,"action"),"probe_exit")||decode(item,&m)||
       vr_timestamp(vr_string(item,"expires_at"),&m.expires)||
       (domain&&*domain&&(strcmp(domain,m.domain)||strcmp(d.rule,m.rule)||strcmp(d.exit_node,m.exit)))||
       (ip&&*ip&&strcmp(ip,m.ip)))goto done;
    if(m.expires>now+VR_MAPPING_TTL)m.expires=now+VR_MAPPING_TTL;
    m.used=vr_millis();pthread_mutex_lock(&e->mutex);size_t target=0;
    if(revision!=e->policy_revision||!usable(e,&m,now)){pthread_mutex_unlock(&e->mutex);goto done;}
    /* One authoritative item replaces both domain and fake-IP collisions. */
    for(size_t i=0;i<VR_MAPPING_MAX;i++) {
        vr_mapping *old=&e->mappings[i];
        if(!strcmp(old->domain,m.domain)||!strcmp(old->ip,m.ip))memset(old,0,sizeof(*old));
    }
    for(size_t i=0;i<VR_MAPPING_MAX;i++) {
        if(e->mappings[i].expires<=now){target=i;break;}
        if(e->mappings[i].used<e->mappings[target].used)target=i;
    }
    e->mappings[target]=m;e->mapping_dirty++;pthread_mutex_unlock(&e->mutex);*out=m;rc=0;
done:
    pthread_mutex_lock(&e->mutex);e->mapping_pending[slot].retry=vr_millis()+(rc?1000:0);e->mapping_pending[slot].busy=0;pthread_mutex_unlock(&e->mutex);
    if(rc&&e->service){char event[240];snprintf(event,sizeof(event),"Fake IP lookup failed: %.160s",*error?error:"invalid authoritative mapping");vr_service_log(e->service,"dns",event);}
    json_object_put(response);return rc;
}

int vr_mapping_lookup(vr_engine *e,const char *domain,const char *ip,vr_decision d,vr_mapping *out)
{
    pthread_mutex_lock(&e->mutex);
    int rc=domain&&*domain&&d.revision!=e->policy_revision?-1:lookup(e,domain,ip,d,out);
    pthread_mutex_unlock(&e->mutex);return rc;
}
