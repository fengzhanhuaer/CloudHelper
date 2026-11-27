#include "packet.h"
#include <string.h>

static uint16_t u16(const uint8_t *p){return (uint16_t)((p[0]<<8)|p[1]);}
static void p16(uint8_t *p,uint16_t v){p[0]=(uint8_t)(v>>8);p[1]=(uint8_t)v;}

static uint32_t replace_word(uint32_t sum,uint16_t before,uint16_t after)
{return sum+(uint16_t)~before+after;}

static uint16_t finish_checksum(uint32_t sum)
{
    while(sum>>16)sum=(sum&65535)+(sum>>16);
    return (uint16_t)~sum;
}

static uint16_t transport_checksum(const uint8_t *b,size_t ihl,size_t n)
{
    uint32_t sum=(uint16_t)~vr_checksum(b+12,8);sum+=b[9];sum+=(uint16_t)(n-ihl);sum+=(uint16_t)~vr_checksum(b+ihl,n-ihl);
    return finish_checksum(sum);
}

int vr_packet_parse(const uint8_t *b,size_t n,vr_packet *p)
{
    if(n<20 || b[0]>>4!=4)return -1;
    size_t ihl=(b[0]&15)*4,length=u16(b+2);
    if(ihl<20 || ihl>n || length!=n || length<ihl || (u16(b+6)&0x3fff) || vr_checksum(b,ihl))return -1;
    memset(p,0,sizeof(*p));p->ihl=ihl;p->length=length;p->protocol=b[9];memcpy(&p->src,b+12,4);memcpy(&p->dst,b+16,4);
    size_t size=n-ihl;const uint8_t *transport=b+ihl;
    if(p->protocol==6) {if(size<20 || (transport[12]>>4)*4u<20 || (transport[12]>>4)*4u>size)return -1;}
    else if(p->protocol==17) {if(size<8 || u16(transport+4)!=size)return -1;}
    else if(p->protocol==1) {
        if(size<8 || (transport[0]!=0 && transport[0]!=8) || transport[1]!=0 || vr_checksum(transport,size))return -1;
        p->sport=p->dport=u16(transport+4);return 0;
    } else return -1;
    if((p->protocol==6||u16(transport+6))&&transport_checksum(b,ihl,n))return -1;
    p->sport=u16(transport);p->dport=u16(transport+2);return 0;
}

int vr_packet_rewrite(uint8_t *b,size_t n,uint32_t src,uint32_t dst,uint16_t sport,uint16_t dport)
{
    vr_packet p;if(vr_packet_parse(b,n,&p))return -1;
    return vr_packet_rewrite_parsed(b,n,&p,src,dst,sport,dport);
}

int vr_packet_rewrite_parsed(uint8_t *b,size_t n,const vr_packet *parsed,uint32_t src,uint32_t dst,uint16_t sport,uint16_t dport)
{
    if(!parsed||parsed->length!=n||parsed->ihl<20||parsed->ihl>n||
       (parsed->protocol!=1&&parsed->protocol!=6&&parsed->protocol!=17)||n-parsed->ihl<(parsed->protocol==6?20u:8u))return -1;
    /* The caller validated both checksums. Apply the one's-complement delta
       for the modified words so the payload is not traversed again. */
    uint8_t addresses[8];
    memcpy(addresses,&src,4);memcpy(addresses+4,&dst,4);
    uint32_t delta=0;
    for(size_t i=0;i<sizeof(addresses);i+=2)delta=replace_word(delta,u16(b+12+i),u16(addresses+i));
    p16(b+10,finish_checksum((uint16_t)~u16(b+10)+delta));
    memcpy(b+12,addresses,sizeof(addresses));

    uint8_t *t=b+parsed->ihl;
    if(parsed->protocol==1) {
        p16(t+2,finish_checksum(replace_word((uint16_t)~u16(t+2),u16(t+4),sport)));
        p16(t+4,sport);
        return 0;
    }
    size_t off=parsed->protocol==6?16:6;
    uint16_t old=u16(t+off);
    if(!old&&parsed->protocol==17) {
        /* IPv4 UDP may arrive with the checksum omitted. Keep generating
           the checksum on NAT egress as the previous full rewrite did. */
        p16(t,sport);p16(t+2,dport);
        uint16_t checksum=transport_checksum(b,parsed->ihl,n);
        p16(t+off,checksum?checksum:65535);
        return 0;
    }
    uint32_t sum=(uint16_t)~old+delta;
    sum=replace_word(sum,u16(t),sport);
    sum=replace_word(sum,u16(t+2),dport);
    uint16_t checksum=finish_checksum(sum);
    if(!checksum&&parsed->protocol==17)checksum=65535;
    p16(t+off,checksum);
    p16(t,sport);p16(t+2,dport);
    return 0;
}
