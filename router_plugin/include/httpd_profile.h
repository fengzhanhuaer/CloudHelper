#ifndef CHVR_HTTPD_PROFILE_H
#define CHVR_HTTPD_PROFILE_H
#include <stdint.h>
#include <stddef.h>
/* RT-BE86U 3.0.0.6.102_39436, ARM32 EXEC. The service and constructor
 * independently pin the entire vendor executable before using these values.
 * These are data/callback addresses, never instructions to patch. */
#define CHVR_BE86U_HTTPD_SHA256 "8804d44c2633046d7740d356e7bdcd5e46b1d2b3ac9b1e967dda01e99150823e"
#define CHVR_BE86U_HTTPD_BYTES 804160
#define CHVR_BE86U_ASP 0xdd1c8
#define CHVR_BE86U_SSL_ENABLED 0xe41dc
#define CHVR_BE86U_SSL_FD 0x1285fc
#define CHVR_BE86U_ASP_FIELDS {0xc49a0,0xb54ec,0xdc0d1,0x36770,0x20cf0,0x215e0}
static inline int chvr_httpd_fields_match(const void *table,const uintptr_t expected[6])
{
    const unsigned char *p=table;
    for(size_t i=0;i<6;i++) {
        uintptr_t actual;
        /* memcpy also avoids assuming an alias between object/function
         * pointers and an integer array. Caller validates the mapped range. */
        __builtin_memcpy(&actual,p+i*sizeof(actual),sizeof(actual));
        if(actual!=expected[i])return 0;
    }
    return 1;
}
#endif
