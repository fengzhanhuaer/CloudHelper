#include "httpd_profile.h"
#include "httpd_layout.h"
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
    /* A synthetic future build relocates both table and callbacks; discovery
     * must follow structure rather than today's fixture addresses/digests. */
    unsigned char elf[1024]={0};
    memcpy(elf,"\177ELF\1\1",6);elf[16]=2;elf[18]=40;elf[28]=52;elf[42]=32;elf[44]=2;
    unsigned code[]={1,0,0x10000,0,512,512,5,4096};
    unsigned data[]={1,512,0x20200,0,512,512,6,4096};
    memcpy(elf+52,code,sizeof(code));memcpy(elf+84,data,sizeof(data));
    memcpy(elf+256,"**.asp*",8);memcpy(elf+280,"text/html",10);
    unsigned fields[]={0x10100,0x10118,0,0x10140,0x10160,0x10180};
    memcpy(elf+512,fields,sizeof(fields));
    chvr_httpd_layout layout;assert(!chvr_httpd_detect(elf,sizeof(elf),&layout));assert(layout.asp==0x20200);
    memcpy(elf+544,fields,sizeof(fields));assert(chvr_httpd_detect(elf,sizeof(elf),&layout));
    memset(elf+544,0,sizeof(fields));elf[512+20]=0;elf[512+21]=0;elf[512+22]=0;
    assert(chvr_httpd_detect(elf,sizeof(elf),&layout));
    assert(chvr_httpd_detect(elf,70,&layout));
    puts("PASS: hidden handler rejects changed pattern/type/headers/input/output/auth");
    return 0;
}
