#include "chvr.h"
#include <ctype.h>
#include <arpa/inet.h>
#include <string.h>

static unsigned be16(const uint8_t *p){return (unsigned)p[0]*256+p[1];}

/* 0 means incomplete; 1 means complete, non-TLS, malformed, or over budget.
 * No untrusted length can allocate memory or escape the 16 KiB input budget.
 * Reassemble handshake bytes across TLS records as well as TCP reads. */
int vr_tls_server_name(const uint8_t *wire,size_t n,char out[254])
{
    uint8_t hello[16384];size_t used=0,at=0;*out=0;
    while(at<n) {
        if(wire[at]!=22)return 1;
        if(n-at<5)return n>=16384;
        if(wire[at+1]!=3)return 1;
        size_t len=be16(wire+at+3);
        if(!len||len>sizeof(hello)-used)return 1;
        if(n-at-5<len)return n>=16384;
        memcpy(hello+used,wire+at+5,len);used+=len;at+=5+len;
        if(used<4)continue;
        size_t need=4+((size_t)hello[1]<<16)+((size_t)hello[2]<<8)+hello[3];
        if(hello[0]!=1||need>sizeof(hello)||need<38)return 1;
        if(used<need)continue;
        size_t p=38;if(p>=need)return 1;
        size_t k=hello[p++];if(k>need-p)return 1;p+=k;
        if(need-p<2)return 1;
        k=be16(hello+p);p+=2;
        if(!k||(k&1)||k>need-p)return 1;
        p+=k;
        if(p>=need)return 1;
        k=hello[p++];if(!k||k>need-p)return 1;
        p+=k;
        if(p==need)return 1;
        if(need-p<2)return 1;
        k=be16(hello+p);p+=2;if(k!=need-p)return 1;
        while(need-p>=4) {
            unsigned type=be16(hello+p);k=be16(hello+p+2);p+=4;
            if(k>need-p)return 1;
            if(type==0) {
                if(k<5||be16(hello+p)!=k-2)return 1;
                size_t q=p+2,end=p+k;
                while(end-q>=3) {
                    unsigned kind=hello[q];size_t size=be16(hello+q+1);q+=3;
                    if(!size||size>end-q)return 1;
                    if(kind==0) {
                        if(size>253)return 1;
                        char name[254];size_t label=0;int dot=0;
                        for(size_t i=0;i<size;i++) {
                            unsigned char ch=hello[q+i];
                            if(ch=='.') {if(!label||label>63||name[i-1]=='-')return 1;label=0;dot=1;}
                            else {if(!((ch>='a'&&ch<='z')||(ch>='A'&&ch<='Z')||(ch>='0'&&ch<='9')||ch=='-')||(!label&&ch=='-'))return 1;label++;}
                            name[i]=(char)tolower(ch);
                        }
                        if(!dot||!label||label>63||name[size-1]=='-')return 1;
                        name[size]=0;struct in_addr ip;if(inet_pton(AF_INET,name,&ip)==1)return 1;
                        memcpy(out,name,size+1);return 1;
                    }
                    q+=size;
                }
                return 1;
            }
            p+=k;
        }
        return 1;
    }
    return n>=16384;
}
