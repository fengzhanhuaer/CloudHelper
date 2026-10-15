#include "transport.h"
#include <string.h>
#include <openssl/rand.h>
#include <openssl/sha.h>
#include <openssl/evp.h>

int vr_ws_accept_key(const char *key,char out[29])
{
    unsigned char decoded[32],digest[20];char input[128];
    if(strlen(key)!=24 || key[22]!='=' || key[23]!='=' || EVP_DecodeBlock(decoded,(const unsigned char *)key,24)!=18)return -1;
    strcpy(input,key);strcat(input,"258EAFA5-E914-47DA-95CA-C5AB0DC85B11");
    if(!SHA1((const unsigned char *)input,strlen(input),digest))return -1;
    return EVP_EncodeBlock((unsigned char *)out,digest,20)==28?0:-1;
}

int vr_ws_encode(int opcode,int mask,const void *bytes,size_t n,uint8_t *out,size_t cap,size_t *length)
{
    size_t header=n<126?2:n<=65535?4:10;
    if(n>2*1024*1024 || (opcode!=2 && opcode!=8 && opcode!=9 && opcode!=10) || (opcode>=8 && n>125))return -1;
    size_t total=header+(mask?4:0)+n;if(cap<total)return -1;
    out[0]=(uint8_t)(0x80|opcode);out[1]=(uint8_t)((mask?0x80:0)|(n<126?n:n<=65535?126:127));
    if(header==4){out[2]=(uint8_t)(n>>8);out[3]=(uint8_t)n;}
    if(header==10){for(unsigned i=0;i<8;i++)out[2+i]=(uint8_t)((uint64_t)n>>(56-8*i));}
    if(mask && RAND_bytes(out+header,4)!=1)return -1;
    const uint8_t *p=bytes;
    for(size_t i=0;i<n;i++)out[header+(mask?4:0)+i]=(uint8_t)(p[i]^(mask?out[header+(i&3)]:0));
    *length=total;return 0;
}

int vr_ws_feed(vr_ws_decoder *d,const void *bytes,size_t n,vr_ws_handler fn,void *arg)
{
    const uint8_t *p=bytes;
    while(n) {
        if(!d->header_needed)d->header_needed=2;
        if(d->header_used<d->header_needed) {
            size_t k=d->header_needed-d->header_used;if(k>n)k=n;
            memcpy(d->header+d->header_used,p,k);d->header_used+=k;p+=k;n-=k;
            if(d->header_used<d->header_needed)continue;
            if(d->header_needed==2) {
                uint8_t a=d->header[0],b=d->header[1];
                d->opcode=a&15;d->final=(a&128)!=0;
                if((a&0x70) || ((b&128)!=0)!=d->expect_mask ||
                    (d->opcode!=0 && d->opcode!=2 && d->opcode!=8 && d->opcode!=9 && d->opcode!=10) ||
                    (d->opcode>=8 && (!d->final || (b&127)>125)) ||
                    (d->opcode==0 && !d->fragmented) || (d->opcode==2 && d->fragmented))return -1;
                d->header_needed=2+((b&127)==126?2:(b&127)==127?8:0)+(d->expect_mask?4:0);
                if(d->header_used<d->header_needed)continue;
            }
            unsigned code=d->header[1]&127;uint64_t len=code;
            size_t ext=code==126?2:code==127?8:0;
            if(ext){len=0;for(size_t i=0;i<ext;i++)len=(len<<8)|d->header[2+i];}
            if(len>2*1024*1024 || (code==126 && len<126) || (code==127 && len<=65535))return -1;
            if(d->expect_mask)memcpy(d->mask,d->header+2+ext,4);
            d->payload_length=(size_t)len;d->payload_used=0;
            if(d->opcode<8) {
                if(d->opcode==2)d->message_length=0;
                if(len>2*1024*1024-d->message_length)return -1;
                d->message_length+=(size_t)len;
            }
        }
        size_t k=d->payload_length-d->payload_used;if(k>n)k=n;
        if(k>4096)k=4096;
        uint8_t chunk[4096];
        for(size_t i=0;i<k;i++)chunk[i]=(uint8_t)(p[i]^(d->expect_mask?d->mask[(d->payload_used+i)&3]:0));
        if(d->opcode>=8){if(k)memcpy(d->control+d->payload_used,chunk,k);}
        else if(k && fn(2,chunk,k,arg))return -1;
        p+=k;n-=k;d->payload_used+=k;
        if(d->payload_used==d->payload_length) {
            if(d->opcode>=8){if(d->opcode==8 && d->payload_length==1)return -1;if(fn(d->opcode,d->control,d->payload_length,arg))return -1;}
            else d->fragmented=!d->final;
            d->header_used=0;d->header_needed=2;d->payload_length=0;d->payload_used=0;
        }
    }
    return 0;
}
