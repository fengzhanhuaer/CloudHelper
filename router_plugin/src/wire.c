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

int vr_decoder_feed(vr_decoder *d, const void *bytes, size_t n, vr_frame_handler fn, void *arg)
{
    const uint8_t *p=bytes;
    while(n) {
        if(!d->needed)d->needed=12;
        if(d->needed>sizeof(d->bytes) || d->used>d->needed)return -1;
        size_t k=d->needed-d->used;if(k>n)k=n;
        memcpy(d->bytes+d->used,p,k);d->used+=k;p+=k;n-=k;
        if(d->used<d->needed)continue;
        if(d->needed==12) {
            if(get16(d->bytes)!=0x5652 || get16(d->bytes+6)>VR_CONTROL_MAX)return -1;
            d->needed=12u+get16(d->bytes+6)+get16(d->bytes+8);
            if(d->used<d->needed)continue;
        }
        if(vr_checksum(d->bytes,d->needed)!=0)return -1;
        vr_frame f={get16(d->bytes+2),get16(d->bytes+4),d->bytes+12,
            d->bytes+12+get16(d->bytes+6),get16(d->bytes+6),get16(d->bytes+8)};
        int rc=fn(&f,arg);d->used=0;d->needed=12;
        if(rc)return rc;
    }
    return 0;
}

int vr_frame_path(const vr_frame *f, char path[VR_PATH_MAX][129], size_t *n)
{
    memset(path,0,VR_PATH_MAX*129);
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
