/* Real management dispatcher: known controller rules, persistent local actions. */
#define main router_service_main
#include "../src/main.c"
#undef main
#include <assert.h>

static json_object *configure(vr_service *s,const char *choices,int ok)
{
    char body[1024];snprintf(body,sizeof(body),"{\"action\":\"configure\",\"csrf\":\"%s\",\"config\":{\"exit_overrides\":%s}}",s->csrf,choices);
    json_object *input=vr_json_parse(body,strlen(body)),*reply=vr_service_dispatch(s,input);json_object_put(input);
    assert(json_object_get_boolean(vr_member(reply,"ok"))==ok);return reply;
}

int main(int argc,char **argv)
{
    vr_service s={0};pthread_mutex_init(&s.mutex,NULL);pthread_cond_init(&s.changed,NULL);vr_config_default(&s.config);
    char dir[]="/tmp/chvr-exits-XXXXXX";assert(mkdtemp(dir));snprintf(s.config_path,sizeof(s.config_path),"%s/config.json",dir);
    memset(s.csrf,'a',48);s.csrf[48]=0;s.generation=7;
    const char *status="{\"ready\":true,\"route_rules\":[{\"id\":\"direct\",\"action\":\"direct\"},{\"id\":\"reject\",\"action\":\"reject\"},{\"id\":\"proxy\",\"action\":\"probe_exit\"}],\"nodes\":[{\"node_id\":\"8\"}]}";
    s.status=vr_json_parse(status,strlen(status));
    /* Browser fixtures can exercise the same CSRF-protected native API. */
    if(argc==2) {
        assert(!strcmp(argv[1],"--stdio"));char line[20000];
        while(fgets(line,sizeof(line),stdin)) {
            json_object *input=vr_json_parse(line,strlen(line));assert(input);
            json_object *reply=vr_service_dispatch(&s,input);puts(json_object_to_json_string_ext(reply,JSON_C_TO_STRING_PLAIN));fflush(stdout);
            json_object_put(input);json_object_put(reply);
        }
        json_object_put(s.status);pthread_cond_destroy(&s.changed);pthread_mutex_destroy(&s.mutex);
        if(!access(s.config_path,F_OK))assert(!unlink(s.config_path));
        assert(!rmdir(dir));return 0;
    }
    assert(argc==1);
    const char *choices="{\"direct\":\"@reject\",\"reject\":\"@direct\",\"proxy\":\"@direct\"}";
    json_object *reply=configure(&s,choices,1);assert(s.generation==7&&json_object_get_boolean(vr_member(vr_member(reply,"status"),"ready")));json_object_put(reply);
    vr_config restored;char error[256];assert(!vr_config_load(&restored,s.config_path,error,sizeof(error))&&restored.exit_overrides.count==3);
    assert(!strcmp(restored.exit_overrides.items[0].node,"@reject"));
    struct stat before,after;assert(!stat(s.config_path,&before));reply=configure(&s,choices,1);json_object_put(reply);
    assert(!stat(s.config_path,&after)&&before.st_mtim.tv_sec==after.st_mtim.tv_sec&&before.st_mtim.tv_nsec==after.st_mtim.tv_nsec);
    const char *invalid[]={"{\"unknown\":\"@direct\"}","{\"proxy\":\"@unknown\"}","{\"proxy\":\"999\"}","{\"direct\":\"8\"}"};
    for(size_t i=0;i<sizeof(invalid)/sizeof(*invalid);i++){reply=configure(&s,invalid[i],0);json_object_put(reply);}
    reply=configure(&s,"{\"proxy\":\"8\"}",1);json_object_put(reply);assert(s.generation==7);
    reply=configure(&s,"{}",1);assert(!s.config.exit_overrides.count&&s.generation==7);json_object_put(reply);
    assert(!strcmp(vr_string(json_object_array_get_idx(vr_member(s.status,"route_rules"),0),"action"),"direct"));
    json_object_put(s.status);pthread_cond_destroy(&s.changed);pthread_mutex_destroy(&s.mutex);assert(!unlink(s.config_path)&&!rmdir(dir));
    puts("PASS: local direct/reject API, known-rule validation, persistence, no-op write, reset and worker generation retained");return 0;
}
