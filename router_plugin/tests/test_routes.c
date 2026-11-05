#include "platform.h"
#include <assert.h>
#include <string.h>
int main(int argc,char **argv)
{
    assert(!vr_platform_routes_update(NULL,NULL));
    assert(argc==3||argc==5);json_object *old=vr_cache_load(argv[1]),*next=vr_cache_load(argv[2]);assert(old&&next);
    int rc;
    if(argc==5) {
        vr_exit_overrides before={0},after={0};
        if(*argv[3]){before.count=1;strcpy(before.items[0].rule,"direct");strcpy(before.items[0].node,argv[3]);}
        if(*argv[4]){after.count=1;strcpy(after.items[0].rule,"direct");strcpy(after.items[0].node,argv[4]);}
        rc=vr_platform_exit_routes(old,next,&before,&after);
        assert(!strcmp(vr_string(json_object_array_get_idx(vr_member(next,"route_rules"),0),"action"),"direct"));
    } else rc=vr_platform_routes_update(old,next);
    json_object_put(old);json_object_put(next);return rc?1:0;
}
