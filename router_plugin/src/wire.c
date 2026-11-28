#include "chvr.h"
#include <string.h>

static uint16_t get16(const uint8_t *p) {return (uint16_t)((p[0]<<8)|p[1]);}
static void put16(uint8_t *p, uint16_t v) {p[0]=(uint8_t)(v>>8);p[1]=(uint8_t)v;}

uint16_t vr_checksum(const void *bytes, size_t n)
{
    const uint8_t *p=bytes;uint32_t sum=0;
    while(n>=2){sum+=get16(p);p+=2;n-=2;}
    if(n)sum+=(uint32_t)*p<<8;
    while(sum>>16)sum=(sum&65535)+(sum>>16);
    return (uint16_t)~sum;
}

int vr_frame_encode(const vr_frame *f, uint8_t *dst, size_t cap, size_t *n)
{
    size_t len=12+f->control_len+f->data_len;
    if(f->control_len>VR_CONTROL_MAX || f->data_len>VR_DATA_MAX || cap<len ||
        (f->control_len && !f->control) || (f->data_len && !f->data))return -1;
    put16(dst,0x5652);put16(dst+2,f->main);put16(dst+4,f->sub);
    put16(dst+6,(uint16_t)f->control_len);put16(dst+8,(uint16_t)f->data_len);put16(dst+10,0);
    if(f->control_len)memcpy(dst+12,f->control,f->control_len);
    if(f->data_len)memcpy(dst+12+f->control_len,f->data,f->data_len);
    put16(dst+10,vr_checksum(dst,len));*n=len;return 0;
}

static int decode_frame(const uint8_t *p,size_t n,vr_frame_handler fn,void *arg)
{
    if(vr_checksum(p,n)!=0)return -5;
    size_t control=get16(p+6);
    vr_frame f={get16(p+2),get16(p+4),p+12,p+12+control,control,get16(p+8)};
    return fn(&f,arg);
}

int vr_decoder_feed(vr_decoder *d, const void *bytes, size_t n, vr_frame_handler fn, void *arg)
{
    const uint8_t *p=bytes;
    while(n) {
        /* Complete frames can borrow the current TLS read buffer for the
           duration of the callback. Only fragments need the decoder buffer. */
        if(!d->used&&n>=12) {
            if(get16(p)!=0x5652)return -3;
            size_t control=get16(p+6);if(control>VR_CONTROL_MAX)return -4;
            size_t length=12u+control+get16(p+8);
            if(length<=n) {
                if(length>d->peak)d->peak=length;
                int rc=decode_frame(p,length,fn,arg);if(rc)return rc;
                p+=length;n-=length;continue;
            }
        }
        if(!d->needed)d->needed=12;
        if(d->needed>sizeof(d->bytes) || d->used>d->needed)return -2;
        size_t k=d->needed-d->used;if(k>n)k=n;
        memcpy(d->bytes+d->used,p,k);d->used+=k;p+=k;n-=k;
        if(d->used>d->peak)d->peak=d->used;
        if(d->used<d->needed)continue;
        if(d->needed==12) {
            if(get16(d->bytes)!=0x5652)return -3;
            if(get16(d->bytes+6)>VR_CONTROL_MAX)return -4;
            d->needed=12u+get16(d->bytes+6)+get16(d->bytes+8);
            if(d->used<d->needed)continue;
        }
        int rc=decode_frame(d->bytes,d->needed,fn,arg);d->used=0;d->needed=12;
        if(rc)return rc;
    }
    return 0;
}

static int path_utf8(const uint8_t *p,size_t n)
{
    for(size_t i=0;i<n;) {
        unsigned c=p[i++],extra=0,min=0;if(!c)return 0;if(c<128)continue;
        if(c>=0xc2&&c<=0xdf){extra=1;min=0x80;c&=31;}
        else if(c>=0xe0&&c<=0xef){extra=2;min=0x800;c&=15;}
        else if(c>=0xf0&&c<=0xf4){extra=3;min=0x10000;c&=7;}else return 0;
        if(extra>n-i)return 0;
        while(extra--){unsigned b=p[i++];if((b&0xc0)!=0x80)return 0;c=(c<<6)|(b&63);}
        if(c<min||c>0x10ffff||(c>=0xd800&&c<=0xdfff))return 0;
    }
    return 1;
}

int vr_path_binary(char path[VR_PATH_MAX][129],size_t count,uint8_t *out,size_t cap,size_t *length)
{
    if(count<2||count>VR_PATH_MAX||cap<6)return -1;
    out[0]=0;out[1]='P';out[2]=1;out[3]=0;put16(out+4,(uint16_t)count);size_t at=6;
    for(size_t i=0;i<count;i++) {
        const char *end=memchr(path[i],0,129);if(!end)return -1;
        size_t n=(size_t)(end-path[i]);if(!n||n>128||!path_utf8((uint8_t *)path[i],n)||cap-at<n+2)return -1;
        for(size_t j=0;j<i;j++)if(!strcmp(path[i],path[j]))return -1;
        put16(out+at,(uint16_t)n);at+=2;memcpy(out+at,path[i],n);at+=n;
    }
    *length=at;return 0;
}

int vr_frame_path(const vr_frame *f, char path[VR_PATH_MAX][129], size_t *n)
{
    memset(path,0,VR_PATH_MAX*129);
    if(f->control_len&&f->control[0]==0) {
        const uint8_t *b=f->control;size_t len=f->control_len,at=6;
        if(len<6||b[1]!='P'||b[2]!=1||b[3]!=0)return -1;
        size_t count=get16(b+4);if(count<2||count>VR_PATH_MAX)return -1;
        for(size_t i=0;i<count;i++) {
            if(len-at<2)return -1;
            size_t size=get16(b+at);at+=2;
            if(!size||size>128||size>len-at||!path_utf8(b+at,size))return -1;
            memcpy(path[i],b+at,size);at+=size;
            for(size_t j=0;j<i;j++)if(!strcmp(path[i],path[j]))return -1;
        }
        if(at!=len)return -1;
        *n=count;return 0;
    }
    json_object *o=vr_json_parse(f->control,f->control_len),*a=vr_member(o,"path");
    int rc=-1;
    if(!a || !json_object_is_type(a,json_type_array))goto done;
    size_t count=json_object_array_length(a);
    if(count<2 || count>VR_PATH_MAX)goto done;
    for(size_t i=0;i<count;i++) {
        json_object *v=json_object_array_get_idx(a,i);
        if(!json_object_is_type(v,json_type_string))goto done;
        const char *s=json_object_get_string(v);
        if(!*s || (size_t)json_object_get_string_len(v)!=strlen(s) || vr_copy(path[i],129,s))goto done;
        for(size_t j=0;j<i;j++)if(!strcmp(path[i],path[j]))goto done;
    }
    *n=count;rc=0;
done:json_object_put(o);return rc;
}
