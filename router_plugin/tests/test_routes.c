#include "platform.h"
#include <assert.h>
int main(int argc,char **argv)
{
    assert(!vr_platform_routes_update(NULL,NULL));
    assert(argc==3);json_object *old=vr_cache_load(argv[1]),*next=vr_cache_load(argv[2]);assert(old&&next);
    int rc=vr_platform_routes_update(old,next);json_object_put(old);json_object_put(next);return rc?1:0;
}
