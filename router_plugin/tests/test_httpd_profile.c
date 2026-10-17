#include "httpd_profile.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
int main(void)
{
    for(size_t p=0;p<sizeof(chvr_be86u_profiles)/sizeof(chvr_be86u_profiles[0]);p++) {
    const uintptr_t *expected=chvr_be86u_profiles[p].fields;
    uintptr_t table[6];
    memcpy(table,expected,sizeof(table));
    assert(chvr_httpd_fields_match(table,expected));
    for(unsigned i=0;i<6;i++) {
        table[i]^=4;
        assert(!chvr_httpd_fields_match(table,expected));
        table[i]=expected[i];
    }
    }
    puts("PASS: hidden handler rejects changed pattern/type/headers/input/output/auth");
    return 0;
}
