#define _GNU_SOURCE
#include "chvr.h"
#include "transport.h"
#include "packet.h"
#include <arpa/inet.h>
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>
#include <openssl/evp.h>

static unsigned frames;
static int frame_received(const vr_frame *f,void *arg)
{
    (void)arg;char path[VR_PATH_MAX][129];size_t n;
    assert(f->main==1 && f->sub==0 && f->data_len==3);
    assert(!memcmp(f->data,"abc",3));
    assert(!vr_frame_path(f,path,&n) && n==2 && !strcmp(path[0],"1") && !strcmp(path[1],"2"));
    frames++;return 0;
}

static void test_wire(void)
{
    uint8_t wire[256];size_t n;
    const char *control="{\"path\":[\"1\",\"2\"]}";
    vr_frame f={1,0,(const uint8_t *)control,(const uint8_t *)"abc",strlen(control),3};
    assert(!vr_frame_encode(&f,wire,sizeof(wire),&n));
    assert(wire[0]==0x56 && wire[1]==0x52 && !vr_checksum(wire,n));
    for(size_t split=0;split<=n;split++) {
        vr_decoder d={0};frames=0;
        assert(!vr_decoder_feed(&d,wire,split,frame_received,NULL));
        assert(!vr_decoder_feed(&d,wire+split,n-split,frame_received,NULL));
        assert(frames==1);
    }
    vr_decoder d={0};frames=0;
    for(unsigned i=0;i<10;i++)for(size_t j=0;j<n;j++)assert(!vr_decoder_feed(&d,wire+j,1,frame_received,NULL));
    assert(frames==10);
    wire[n-1]^=1;memset(&d,0,sizeof(d));assert(vr_decoder_feed(&d,wire,n,frame_received,NULL)<0);
    wire[6]=0xff;wire[7]=0xff;memset(&d,0,sizeof(d));assert(vr_decoder_feed(&d,wire,12,frame_received,NULL)<0);
    f.control=(const uint8_t *)"{\"path\":[\"1\",\"1\"]}";f.control_len=strlen((const char *)f.control);
    char path[VR_PATH_MAX][129];size_t count;assert(vr_frame_path(&f,path,&count)<0);
}

static const uint8_t dns_query[]={0x12,0x34,1,0,0,1,0,0,0,0,0,0,7,'e','x','a','m','p','l','e',3,'c','o','m',0,0,1,0,1};
static void test_dns(void)
{
    vr_question q;uint8_t b[512];size_t n;
    assert(!vr_dns_question(dns_query,sizeof(dns_query),&q));assert(!strcmp(q.name,"example.com")&&q.type==1);
    assert(!vr_dns_reply(dns_query,sizeof(dns_query),b,sizeof(b),0,"198.18.0.5",&n));
    assert(n==sizeof(dns_query)+16 && b[7]==1 && b[n-4]==198 && b[n-1]==5);
    assert(!vr_dns_question(b,n,&q));
    char ip[16];assert(!vr_dns_address4(b,n,ip)&&!strcmp(ip,"198.18.0.5"));
    assert(vr_dns_address4(b,n-1,ip)<0);
    assert(!vr_dns_reply(dns_query,sizeof(dns_query),b,sizeof(b),5,NULL,&n));assert((b[3]&15)==5&&b[7]==0);
    for(size_t i=0;i<sizeof(dns_query);i++)assert(vr_dns_question(dns_query,i,&q)<0);
    memcpy(b,dns_query,sizeof(dns_query));b[12]=0xc0;b[13]=12;assert(vr_dns_question(b,sizeof(dns_query),&q)<0);
    const uint8_t root[]={0,1,1,0,0,1,0,0,0,0,0,0,0,0,2,0,1};
    assert(!vr_dns_question(root,sizeof(root),&q)&&!strcmp(q.name,"."));
    assert(!vr_dns_reply(root,sizeof(root),b,sizeof(b),0,NULL,&n)&&n==sizeof(root));
    unsigned seed=7;
    for(unsigned i=0;i<30000;i++) {
        size_t len=i%sizeof(b);
        for(size_t j=0;j<len;j++){seed=seed*1664525u+1013904223u;b[j]=(uint8_t)(seed>>24);}
        (void)vr_dns_question(b,len,&q);
    }
}

static json_object *parse(const char *s){json_object *o=vr_json_parse(s,strlen(s));assert(o);return o;}

static void test_config(void)
{
    char err[256];vr_config c,updated;vr_config_default(&c);
    assert(!c.enabled && c.port==12040 && !c.allow_wan);
    assert(!strcmp(c.dns_doh,"https://dns.alidns.com/dns-query")&&!strcmp(c.dns_dot,"dns.alidns.com:853")&&!strcmp(c.dns_upstream,"223.5.5.5:53"));
    json_object *o=parse("{\"enabled\":true,\"controller\":\"https://controller.test:443/\",\"node_id\":\"7\",\"node_key\":\"test-key\"}");
    assert(!vr_config_parse(&c,o,NULL,err,sizeof(err)));json_object_put(o);
    assert(!strcmp(c.controller,"https://controller.test:443"));
    o=parse("{\"node_key\":\"\",\"port\":12041}");assert(!vr_config_parse(&updated,o,&c,err,sizeof(err)));json_object_put(o);
    assert(!strcmp(updated.node_key,"test-key")&&updated.port==12041);
    const char *invalid[]={"{\"port\":80}","{\"port\":1.5}","{\"enabled\":\"yes\"}","{\"controller\":\"https://u:p@host\"}","{\"controller\":\"http://host/route\"}","{\"node_id\":\"x\\nInjected\"}","{\"dns_upstream\":\"127.0.0.1:53\"}","{\"unknown\":1}"};
    for(size_t i=0;i<sizeof(invalid)/sizeof(*invalid);i++){o=parse(invalid[i]);assert(vr_config_parse(&updated,o,&c,err,sizeof(err))<0);json_object_put(o);}
    o=vr_config_json(&c,0);assert(!vr_member(o,"node_key"));json_object_put(o);
    char dir[]="/tmp/chvr-test-XXXXXX",file[256];assert(mkdtemp(dir));snprintf(file,sizeof(file),"%s/config.json",dir);
    assert(!vr_config_save(&c,file));struct stat st;assert(!stat(file,&st)&&(st.st_mode&0777)==0600);
    assert(!vr_config_load(&updated,file,err,sizeof(err)));assert(!strcmp(updated.node_key,c.node_key));
    assert(!strcmp(updated.dns_doh,c.dns_doh)&&!strcmp(updated.dns_dot,c.dns_dot)&&!strcmp(updated.dns_upstream,c.dns_upstream));
    unlink(file);rmdir(dir);
    assert(!vr_json_parse("{}{}",4));assert(!vr_json_parse("{\"x\":1,}",8));
    o=parse("{\"x\":\"prefix\\u0000hidden\"}");assert(!*vr_string(o,"x"));json_object_put(o);
}

static void test_policy(void)
{
    json_object *p=parse("{\"route_rules\":[{\"id\":\"reject\",\"action\":\"reject\",\"entries\":[\"domain_suffix:blocked.test\"]},{\"id\":\"exit\",\"action\":\"probe_exit\",\"exit_node_id\":\"2\",\"entries\":[\"domain_suffix:example.com\",\"cidr:203.0.113.0/24\"]}]}" );
    assert(vr_policy_match(p,"a.blocked.test",NULL).action==VR_REJECT);
    assert(vr_policy_match(p,"notexample.com",NULL).action==VR_DIRECT);
    vr_decision d=vr_policy_match(p,"www.EXAMPLE.com",NULL);assert(d.action==VR_PROXY&&!strcmp(d.exit_node,"2"));
    assert(vr_policy_match(p,"other.test","203.0.113.5").action==VR_PROXY);
    assert(vr_policy_match(p,"other.test","203.0.114.5").action==VR_DIRECT);
    assert(vr_policy_needs_ip(p,"other.test"));assert(!vr_policy_needs_ip(p,"www.example.com"));json_object_put(p);
    p=parse("{\"route_rules\":[{\"action\":\"reject\",\"entries\":[\"cidr:203.0.113.0/24\"]},{\"action\":\"direct\",\"entries\":[\"domain_suffix:example.com\"]}]}");
    assert(vr_policy_needs_ip(p,"example.com"));assert(vr_policy_match(p,"example.com","203.0.113.5").action==VR_REJECT);json_object_put(p);
}

static void base64url(const unsigned char *b,int n,char *out)
{
    int len=EVP_EncodeBlock((unsigned char *)out,b,n);
    while(len&&out[len-1]=='=')out[--len]=0;
    for(int i=0;i<len;i++){if(out[i]=='+')out[i]='-';if(out[i]=='/')out[i]='_';}
}

static void test_auth(void)
{
    char h[65];assert(!vr_hmac("key","The quick brown fox jumps over the lazy dog",h));
    assert(!strcmp(h,"f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"));
    EVP_PKEY_CTX *gen=EVP_PKEY_CTX_new_id(EVP_PKEY_ED25519,NULL);EVP_PKEY *key=NULL;
    assert(gen&&EVP_PKEY_keygen_init(gen)==1&&EVP_PKEY_keygen(gen,&key)==1);EVP_PKEY_CTX_free(gen);
    unsigned char pub[32],sig[64];size_t len=32;assert(EVP_PKEY_get_raw_public_key(key,pub,&len)==1);
    char pub64[64],body[1024],ticket[2048],sig64[128];EVP_EncodeBlock((unsigned char *)pub64,pub,32);
    snprintf(body,sizeof(body),"{\"version\":\"route-auth-v3\",\"route_id\":\"vrouter-test\",\"user_public_key\":\"%s\",\"from_node_id\":\"1\",\"to_node_id\":\"2\",\"ticket_id\":\"t\",\"client_entry_id\":\"c\",\"issued_at\":\"2026-01-01T00:00:00Z\",\"expires_at\":\"2026-01-02T00:00:00Z\"}",pub64);
    EVP_MD_CTX *ctx=EVP_MD_CTX_new();len=64;assert(EVP_DigestSignInit(ctx,NULL,NULL,NULL,key)==1&&EVP_DigestSign(ctx,sig,&len,(unsigned char *)body,strlen(body))==1);EVP_MD_CTX_free(ctx);EVP_PKEY_free(key);
    base64url((unsigned char *)body,(int)strlen(body),ticket);base64url(sig,64,sig64);strcat(ticket,".");strcat(ticket,sig64);
    time_t expiry;time_t now=1767268800;
    assert(!vr_ticket_verify(ticket,pub64,"vrouter-test","1","2",now,&expiry));
    assert(vr_ticket_verify(ticket,pub64,"vrouter-test","1","3",now,&expiry)<0);
    assert(vr_ticket_verify(ticket,pub64,"vrouter-test","1","2",now+86400,&expiry)<0);
    ticket[2]=ticket[2]=='A'?'B':'A';assert(vr_ticket_verify(ticket,pub64,"vrouter-test","1","2",now,&expiry)<0);
}

static size_t ws_received;
static int ws_payload(int opcode,const uint8_t *p,size_t n,void *arg)
{
    (void)arg;assert(opcode==2);for(size_t i=0;i<n;i++)assert(p[i]==(uint8_t)((ws_received+i)%251));ws_received+=n;return 0;
}
static void test_websocket(void)
{
    char accept[29];assert(!vr_ws_accept_key("dGhlIHNhbXBsZSBub25jZQ==",accept));assert(!strcmp(accept,"s3pPLMBiTxaQ9kYGzzhZRbK+xOo="));
    uint8_t data[2048],wire[2062];for(size_t i=0;i<sizeof(data);i++)data[i]=(uint8_t)(i%251);
    for(int mask=0;mask<2;mask++){
        size_t n;assert(!vr_ws_encode(2,mask,data,sizeof(data),wire,sizeof(wire),&n));
        vr_ws_decoder d={0};d.expect_mask=mask;ws_received=0;
        for(size_t i=0;i<n;i++)assert(!vr_ws_feed(&d,wire+i,1,ws_payload,NULL));
        assert(ws_received==sizeof(data));
        memset(&d,0,sizeof(d));d.expect_mask=!mask;assert(vr_ws_feed(&d,wire,n,ws_payload,NULL)<0);
    }
    uint8_t invalid[]={0x82,127,0x80,0,0,0,0,0,0,0};vr_ws_decoder d={0};assert(vr_ws_feed(&d,invalid,sizeof(invalid),ws_payload,NULL)<0);
    const char *header="HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n";char value[32];assert(vr_http_field(header,"upgrade",value,sizeof(value))&&!strcmp(value,"websocket"));
    assert(vr_http_token(header,"connection","upgrade"));
    assert(vr_http_token("HTTP/1.1 101 OK\r\nConnection: keep-alive, Upgrade\r\n\r\n","Connection","upgrade"));
    assert(!vr_http_token("HTTP/1.1 101 OK\r\nConnection: not-upgrade\r\n\r\n","Connection","upgrade"));
    assert(!vr_http_token("HTTP/1.1 101 OK\r\nConnection: upgrade\r\nConnection: close\r\n\r\n","Connection","upgrade"));
}

static size_t hex_bytes(const char *hex,uint8_t *out)
{size_t n=strlen(hex)/2;for(size_t i=0;i<n;i++){unsigned v;assert(sscanf(hex+i*2,"%2x",&v)==1);out[i]=(uint8_t)v;}return n;}

static void test_packet(void)
{
    const char *original[]={
        "4500002b0001400040067f99c0a83464c6120014ad9c003500000001000000005002100072770000616263",
        "450000210001400040117f98c0a83464c6120014ad9c0035000d52fd68656c6c6f",
        "450000210001400040017fa8c0a83464c612001408000690ad9c000168656c6c6f"};
    const char *translated[]={
        "4500002b00014000400638a9c612000bcb0071054e2000350000000100000000500210008b030000616263",
        "4500002100014000401138a8c612000bcb0071054e200035000d6b8968656c6c6f",
        "4500002100014000400138b8c612000bcb0071050800660c4e20000168656c6c6f"};
    struct in_addr src,dst;assert(inet_pton(AF_INET,"198.18.0.11",&src)==1&&inet_pton(AF_INET,"203.0.113.5",&dst)==1);
    for(unsigned i=0;i<3;i++) {
        uint8_t b[64],expected[64];size_t n=hex_bytes(original[i],b);assert(hex_bytes(translated[i],expected)==n);
        vr_packet p;assert(!vr_packet_parse(b,n,&p)&&p.sport==44444);
        assert(vr_packet_parse(b,n-1,&p)<0);b[n-1]^=1;assert(vr_packet_parse(b,n,&p)<0);b[n-1]^=1;
        assert(!vr_packet_rewrite(b,n,src.s_addr,dst.s_addr,20000,53));assert(!memcmp(b,expected,n));
        assert(!vr_packet_parse(b,n,&p)&&p.sport==20000&&p.src==src.s_addr&&p.dst==dst.s_addr);
    }
}

int main(void)
{
    test_wire();test_dns();test_config();test_policy();test_auth();test_websocket();test_packet();
    puts("PASS: VR/WebSocket framing, DNS bounds, configuration, policy, Ed25519/HMAC, TCP/UDP/ICMP NAT checksums");return 0;
}
