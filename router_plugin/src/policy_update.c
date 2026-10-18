#include "engine.h"
#include <string.h>

static const char *own_ip(json_object *policy,const char *id)
{
    json_object *nodes=vr_member(policy,"probe_ips");
    for(size_t i=0;i<(size_t)json_object_array_length(nodes);i++) {
        json_object *node=json_object_array_get_idx(nodes,i);
        if(!strcmp(vr_string(node,"node_id"),id))return vr_string(node,"ip");
    }
    return "";
}

int vr_policy_apply_pending(vr_engine *e)
{
    /* Handshakes borrow rule/policy pointers. Let them finish without blocking
       forwarding, and do not start new dials while a publication is pending. */
    for(size_t i=0;i<2;i++)if(e->handshakes[i].started)return 0;
    pthread_mutex_lock(&e->mutex);
    json_object *next=e->next_policy;
    if(!next||vr_millis()<e->policy_retry){pthread_mutex_unlock(&e->mutex);return 0;}
    json_object *policy=vr_member(next,"virtual_router");
    int rebuild=json_object_get_boolean(vr_member(policy,"enabled"))!=json_object_get_boolean(vr_member(e->policy,"enabled")) ||
        strcmp(vr_string(policy,"fake_ip_cidr"),e->cidr) ||
        strcmp(own_ip(policy,e->config.node_id),e->own_ip) ||
        vr_policy_service_port(policy,e->config.node_id,0)!=e->config.port ||
        !vr_policy_equal(vr_member(next,"dns_upstreams"),vr_member(e->snapshot,"dns_upstreams"));
    if(rebuild) {
        e->next_policy=NULL;e->refresh=1;pthread_mutex_unlock(&e->mutex);
        vr_service *s=e->service;pthread_mutex_lock(&s->mutex);
        json_object_put(s->pending_policy);s->pending_policy=next;s->pending_generation=e->generation;
        pthread_mutex_unlock(&s->mutex);return 2;
    }
    json_object_get(next);pthread_mutex_unlock(&e->mutex);
    /* Kernel updates run outside the publication lock; the old policy remains
       active until all additions/removals succeed. Queue producers can only
       replace their pending reference, never the applied engine snapshot. */
    if(vr_platform_routes_update(e->policy,policy)) {
        pthread_mutex_lock(&e->mutex);e->policy_retry=vr_millis()+5000;json_object_put(next);pthread_mutex_unlock(&e->mutex);
        vr_service_log(e->service,"controller","Route update failed; retaining current policy and links");return 0;
    }
    pthread_mutex_lock(&e->mutex);
    vr_links_reconcile(e,policy);
    int rules_changed=!vr_policy_equal(vr_member(e->policy,"route_rules"),vr_member(policy,"route_rules"));
    json_object *old=e->snapshot;e->snapshot=next;e->policy=policy;e->policy_revision++;
    if(rules_changed)vr_mapping_reconcile(e);
    if(e->next_policy==next){json_object_put(e->next_policy);e->next_policy=NULL;}
    json_object_put(old);pthread_mutex_unlock(&e->mutex);
    vr_service_log(e->service,"controller","Route policy applied; unchanged carriers and control stream retained");
    return 1;
}
