#ifndef CHVR_HTTPD_LAYOUT_H
#define CHVR_HTTPD_LAYOUT_H
#include <stdint.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* Locate the authenticated ASP entry by ELF segment permissions and handler
 * structure, never by firmware version, digest or a fixed virtual address. */
typedef struct { uint64_t offset,address,filesize,memory;unsigned flags; } chvr_segment;
typedef struct { uintptr_t asp,fields[6]; } chvr_httpd_layout;
static inline int chvr_handler_matches(const void *table,const uintptr_t fields[6])
{
 for(unsigned i=0;i<6;i++){uintptr_t value;memcpy(&value,(const unsigned char *)table+i*sizeof(value),sizeof(value));if(value!=fields[i])return 0;}
 return 1;
}
static inline uint64_t chvr_le(const unsigned char *p,unsigned width)
{uint64_t v=0;for(unsigned i=0;i<width;i++)v|=(uint64_t)p[i]<<(8*i);return v;}
static inline const unsigned char *chvr_at(const unsigned char *b,chvr_segment *s,unsigned count,uint64_t address,size_t bytes,unsigned flags)
{
 for(unsigned i=0;i<count;i++)if((s[i].flags&flags)==flags&&address>=s[i].address&&bytes<=s[i].filesize&&address-s[i].address<=s[i].filesize-bytes)
  return b+s[i].offset+(size_t)(address-s[i].address);
 return NULL;
}
static inline int chvr_httpd_detect(const unsigned char *b,size_t size,chvr_httpd_layout *out)
{
 if(size<64||memcmp(b,"\177ELF",4)||b[5]!=1||(b[4]!=1&&b[4]!=2)||chvr_le(b+16,2)!=2)return -1;
 unsigned width=b[4]==1?4:8,phsize=(unsigned)chvr_le(b+(width==4?42:54),2),count=(unsigned)chvr_le(b+(width==4?44:56),2);
 uint64_t phoff=chvr_le(b+(width==4?28:32),width);chvr_segment segments[32];unsigned loads=0;
 if(count>128||phsize!=(width==4?32u:56u)||phoff>size||(uint64_t)count*phsize>size-phoff)return -1;
 for(unsigned i=0;i<count;i++){
  const unsigned char *p=b+(size_t)phoff+i*phsize;if(chvr_le(p,4)!=1)continue;
  if(loads==32)return -1;
  chvr_segment *s=&segments[loads++];
  s->offset=chvr_le(p+(width==4?4:8),width);s->address=chvr_le(p+(width==4?8:16),width);
  s->filesize=chvr_le(p+(width==4?16:32),width);s->memory=chvr_le(p+(width==4?20:40),width);
  s->flags=(unsigned)chvr_le(p+(width==4?24:4),4);
  if(s->offset>size||s->filesize>size-s->offset||s->memory<s->filesize||s->address+s->memory<s->address)return -1;
 }
 unsigned matches=0;memset(out,0,sizeof(*out));
 for(unsigned i=0;i<loads;i++){
  chvr_segment *s=&segments[i];if((s->flags&6)!=6||s->filesize<6*width)continue;
  for(uint64_t offset=0;offset<=s->filesize-6*width;offset+=width){
   uintptr_t fields[6];for(unsigned j=0;j<6;j++)fields[j]=(uintptr_t)chvr_le(b+s->offset+offset+j*width,width);
   const unsigned char *pattern=chvr_at(b,segments,loads,fields[0],8,4);
   if(!pattern||memcmp(pattern,"**.asp*\0",8))continue;
   const unsigned char *type=chvr_at(b,segments,loads,fields[1],10,4);
   if(!type||memcmp(type,"text/html",9)||!memchr(type,0,10))continue;
   if(fields[2]&&!chvr_at(b,segments,loads,fields[2],1,4))continue;
   int valid=1;for(unsigned j=3;j<6;j++)if(!chvr_at(b,segments,loads,fields[j]&~(uintptr_t)1,4,5))valid=0;
   if(!valid)continue;
   for(unsigned j=0;j<count;j++){
    const unsigned char *p=b+(size_t)phoff+j*phsize;
    if(chvr_le(p,4)==0x6474e552){
     uint64_t begin=chvr_le(p+(width==4?8:16),width),length=chvr_le(p+(width==4?20:40),width);
     uint64_t address=s->address+offset;
     if(length&&begin<=address&&address-begin<length)valid=0;
    }
   }
   if(!valid)continue;
   if(++matches!=1)return -1;
   out->asp=(uintptr_t)(s->address+offset);memcpy(out->fields,fields,sizeof(fields));
  }
 }
 return matches==1?0:-1;
}
static inline int chvr_httpd_file(const char *path,chvr_httpd_layout *out)
{
 FILE *f=fopen(path,"rb");if(!f)return -1;
 int rc=-1;if(fseek(f,0,SEEK_END))goto done;long length=ftell(f);
 if(length<64||length>32*1024*1024||fseek(f,0,SEEK_SET))goto done;
 unsigned char *b=malloc((size_t)length);if(!b)goto done;
 if(fread(b,1,(size_t)length,f)==(size_t)length)rc=chvr_httpd_detect(b,(size_t)length,out);
 free(b);
done:fclose(f);return rc;
}
#endif
