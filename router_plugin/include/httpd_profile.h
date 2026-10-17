#ifndef CHVR_HTTPD_PROFILE_H
#define CHVR_HTTPD_PROFILE_H
#include <stdint.h>
#include <stddef.h>
/* Frozen CI regression fixtures only; not used for installation, startup or
 * runtime handler discovery. RT-BE86U 3.0.0.6.102_39436, ARM32 EXEC. */
#define CHVR_BE86U_HTTPD_SHA256 "8804d44c2633046d7740d356e7bdcd5e46b1d2b3ac9b1e967dda01e99150823e"
#define CHVR_BE86U_HTTPD_BYTES 804160
#define CHVR_BE86U_ASP 0xdd1c8
#define CHVR_BE86U_SSL_ENABLED 0xe41dc
#define CHVR_BE86U_SSL_FD 0x1285fc
#define CHVR_BE86U_SSL_ENABLED_GOT 0xdb9a4
#define CHVR_BE86U_SSL_FD_GOT 0xdb964
#define CHVR_BE86U_ASP_FIELDS {0xc49a0,0xb54ec,0xdc0d1,0x36770,0x20cf0,0x215e0}
/* RT-BE86U 3.0.0.6.102_39125_koolcenter, independently verified ARM32 EXEC. */
#define CHVR_BE86U_KOOL_HTTPD_SHA256 "1b7148dabccb0954b13b8bf70f853afbcd133160cf9f4cb201789bccdb5143e8"
#define CHVR_BE86U_KOOL_HTTPD_BYTES 803932
#define CHVR_BE86U_KOOL_ASP 0xdd0ec
#define CHVR_BE86U_KOOL_SSL_ENABLED 0xe40fc
#define CHVR_BE86U_KOOL_SSL_FD 0x12851c
#define CHVR_BE86U_KOOL_SSL_ENABLED_GOT 0xdb9b8
#define CHVR_BE86U_KOOL_SSL_FD_GOT 0xdb978
#define CHVR_BE86U_KOOL_ASP_FIELDS {0xc438d,0xb5284,0xdc0e5,0x361d0,0x20e18,0x21780}
typedef struct {
    const char *sha256;
    size_t bytes;
    uintptr_t asp,ssl_enabled,ssl_fd,fields[6];
} chvr_httpd_profile;
static const chvr_httpd_profile chvr_be86u_profiles[]={
    {CHVR_BE86U_HTTPD_SHA256,CHVR_BE86U_HTTPD_BYTES,CHVR_BE86U_ASP,
     CHVR_BE86U_SSL_ENABLED,CHVR_BE86U_SSL_FD,CHVR_BE86U_ASP_FIELDS},
    {CHVR_BE86U_KOOL_HTTPD_SHA256,CHVR_BE86U_KOOL_HTTPD_BYTES,CHVR_BE86U_KOOL_ASP,
     CHVR_BE86U_KOOL_SSL_ENABLED,CHVR_BE86U_KOOL_SSL_FD,CHVR_BE86U_KOOL_ASP_FIELDS}
};
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
