#include "httpd_profile.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
int main(void)
{
    uintptr_t expected[6]=CHVR_BE86U_ASP_FIELDS,table[6];
    memcpy(table,expected,sizeof(table));
    assert(chvr_httpd_fields_match(table,expected));
    for(unsigned i=0;i<6;i++) {
        table[i]^=4;
        assert(!chvr_httpd_fields_match(table,expected));
        table[i]=expected[i];
    }
    puts("PASS: hidden handler rejects changed pattern/type/headers/input/output/auth");
    return 0;
}
