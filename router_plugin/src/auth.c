#define _GNU_SOURCE
#include "chvr.h"
#include <ctype.h>
#include <errno.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <openssl/crypto.h>
#include <openssl/evp.h>
#include <openssl/hmac.h>
#include <openssl/pem.h>
#include <openssl/rand.h>
#include <openssl/sha.h>

int vr_replay_validate(json_object *records)
{
    if(!records||!json_object_is_type(records,json_type_object))return -1;
    unsigned legacy=0,timed=0;
    json_object_object_foreach(records,k,v) {
        const char *digest=k;
        if(strlen(k)==65&&k[0]=='t'){timed++;digest++;}else legacy++;
        if(strlen(digest)!=64||!json_object_is_type(v,json_type_int))return -1;
        for(const char *p=digest;*p;p++)if(!isxdigit((unsigned char)*p))return -1;
    }
    return legacy>VR_REPLAY_LEGACY_MAX||timed>VR_REPLAY_TIMED_MAX?-1:0;
}

int vr_replay_record(json_object **records,const char *path,const char *key,
    const char *nonce,time_t ticket_expiry,time_t now)
{
    int timed=!strncmp(nonce,"ts1:",4);time_t expiry=ticket_expiry;
    if(timed) {
        char *end;errno=0;long long issued=strtoll(nonce+4,&end,10);
        if(errno||end==nonce+4||*end!=':'||issued<=0||issued>(long long)now+300||issued<=(long long)now-300)return -1;
        size_t random_length=strlen(end+1);if(random_length<32||random_length>64)return -1;
        for(const char *p=end+1;*p;p++)if(!isxdigit((unsigned char)*p))return -1;
        if(issued+300<(long long)expiry)expiry=(time_t)(issued+300);
    }
    if(expiry<=now)return -1;
    char stored[66];snprintf(stored,sizeof(stored),"%s%s",timed?"t":"",key);
    json_object *fresh=json_object_new_object();unsigned legacy=0,recent=0;int replayed=0;
    json_object_object_foreach(*records,k,v) {
        if(json_object_get_int64(v)<=now)continue;
        if(!strcmp(k,stored))replayed=1;
        if(strlen(k)==65&&k[0]=='t')recent++;else legacy++;
        json_object_object_add(fresh,k,json_object_new_int64(json_object_get_int64(v)));
    }
    if(replayed){json_object_put(fresh);return -2;}
    if(timed?recent>=VR_REPLAY_TIMED_MAX:legacy>=VR_REPLAY_LEGACY_MAX){json_object_put(fresh);return -3;}
    json_object_object_add(fresh,stored,json_object_new_int64(expiry));
    const char *saved=json_object_to_json_string_ext(fresh,JSON_C_TO_STRING_PLAIN);
    if(strlen(saved)>=VR_REPLAY_FILE_MAX||vr_atomic_write(path,saved,strlen(saved))){json_object_put(fresh);return -4;}
    json_object_put(*records);*records=fresh;return 0;
}

static void hex(const unsigned char *b,size_t n,char *out)
{
    static const char digits[]="0123456789abcdef";
    for(size_t i=0;i<n;i++){out[2*i]=digits[b[i]>>4];out[2*i+1]=digits[b[i]&15];}out[2*n]=0;
}

int vr_random_hex(char *dst,size_t n)
{
    unsigned char b[64];if(n>sizeof(b) || RAND_bytes(b,(int)n)!=1)return -1;
    hex(b,n,dst);OPENSSL_cleanse(b,sizeof(b));return 0;
}

int vr_hmac(const char *secret,const char *text,char out[65])
{
    unsigned char b[EVP_MAX_MD_SIZE];unsigned n=0;
    if(strlen(secret)>1024 || !HMAC(EVP_sha256(),secret,(int)strlen(secret),(const unsigned char *)text,strlen(text),b,&n) || n!=32)return -1;
    hex(b,n,out);OPENSSL_cleanse(b,sizeof(b));return 0;
}

int vr_route_id(const char *rule,char out[33])
{
    if(!*rule || strlen(rule)>128)return -1;
    char text[135],digest[65];unsigned char b[32];
    snprintf(text,sizeof(text),"route|%s",rule);
    if(!SHA256((const unsigned char *)text,strlen(text),b))return -1;
    hex(b,32,digest);memcpy(out,"vrouter-",8);memcpy(out+8,digest,24);out[32]=0;return 0;
}

static int unbase64(const char *s,size_t n,unsigned char *out,size_t cap)
{
    if(n>8192 || n%4==1)return -1;
    char tmp[8197];size_t padded=(n+3)&~(size_t)3;
    if(cap<padded/4*3)return -1;
    for(size_t i=0;i<n;i++) {
        unsigned char c=(unsigned char)s[i];
        if(!(isalnum(c)||c=='-'||c=='_'||c=='+'||c=='/'||c=='='))return -1;
        tmp[i]=c=='-'?'+':c=='_'?'/':(char)c;
        if(c=='=' && i+2<n)return -1;
    }
    for(size_t i=n;i<padded;i++)tmp[i]='=';
    tmp[padded]=0;
    int len=EVP_DecodeBlock(out,(unsigned char *)tmp,(int)padded);
    if(len<0)return -1;
    if(padded && tmp[padded-1]=='=')len--;
    if(padded>1 && tmp[padded-2]=='=')len--;
    return len;
}

static EVP_PKEY *public_key(const char *s)
{
    if(strlen(s)>2048)return NULL;
    if(!strncmp(s,"-----BEGIN PUBLIC KEY-----",26)) {
        BIO *b=BIO_new_mem_buf(s,-1);if(!b)return NULL;
        EVP_PKEY *p=PEM_read_bio_PUBKEY(b,NULL,NULL,NULL);BIO_free(b);return p;
    }
    unsigned char bytes[1600];int n=-1;
    if(strlen(s)==64) {
        n=32;
        for(int i=0;i<32;i++) {
            char a=s[2*i],b=s[2*i+1];
            if(!isxdigit((unsigned char)a)||!isxdigit((unsigned char)b)){n=-1;break;}
            bytes[i]=(unsigned char)((isdigit((unsigned char)a)?a-'0':tolower((unsigned char)a)-'a'+10)*16+
                                      (isdigit((unsigned char)b)?b-'0':tolower((unsigned char)b)-'a'+10));
        }
    }
    if(n<0)n=unbase64(s,strlen(s),bytes,sizeof(bytes));
    if(n==32)return EVP_PKEY_new_raw_public_key(EVP_PKEY_ED25519,NULL,bytes,32);
    if(n<=0)return NULL;
    const unsigned char *p=bytes;
    EVP_PKEY *k=d2i_PUBKEY(NULL,&p,n);
    if(p!=bytes+n){EVP_PKEY_free(k);return NULL;}return k;
}

int vr_timestamp(const char *s,time_t *out)
{
    struct tm t={0};char *end=strptime(s,"%Y-%m-%dT%H:%M:%S",&t);
    if(!end)return -1;
    if(*end=='.'){end++;if(!isdigit((unsigned char)*end))return -1;while(isdigit((unsigned char)*end))end++;}
    int offset=0;
    if(*end=='Z' && !end[1])offset=0;
    else if((*end=='+'||*end=='-') && strlen(end)==6 && end[3]==':' &&
       isdigit((unsigned char)end[1]) && isdigit((unsigned char)end[2]) && isdigit((unsigned char)end[4]) && isdigit((unsigned char)end[5])) {
        int h=(end[1]-'0')*10+end[2]-'0',m=(end[4]-'0')*10+end[5]-'0';
        if(h>23||m>59)return -1;
        offset=(h*60+m)*60;
        if(*end=='-')offset=-offset;
    } else return -1;
    time_t value=timegm(&t);if(value<0)return -1;*out=value-offset;return 0;
}

int vr_ticket_verify(const char *ticket,const char *pubkey,const char *route,const char *from,const char *to,time_t now,time_t *expires)
{
    const char *dot=strchr(ticket,'.');if(!dot || strchr(dot+1,'.') || strlen(ticket)>8192)return -1;
    unsigned char body[8192],sig[128];int bn=unbase64(ticket,(size_t)(dot-ticket),body,sizeof(body)),sn=unbase64(dot+1,strlen(dot+1),sig,sizeof(sig));
    if(bn<=0 || sn!=64)return -1;
    EVP_PKEY *key=public_key(pubkey);EVP_MD_CTX *ctx=EVP_MD_CTX_new();int valid=0;
    if(key && ctx && EVP_PKEY_base_id(key)==EVP_PKEY_ED25519 && EVP_DigestVerifyInit(ctx,NULL,NULL,NULL,key)==1)
        valid=EVP_DigestVerify(ctx,sig,(size_t)sn,body,(size_t)bn)==1;
    EVP_MD_CTX_free(ctx);EVP_PKEY_free(key);if(!valid)return -1;
    json_object *o=vr_json_parse(body,(size_t)bn);int rc=-1;time_t issued,expiry;
    const char *version=vr_string(o,"version");
    if((strcmp(version,"route-auth-v2") && strcmp(version,"route-auth-v3")) ||
        strcmp(vr_string(o,"route_id"),route) || strcmp(vr_string(o,"user_public_key"),pubkey) ||
        strcmp(vr_string(o,"from_node_id"),from) || strcmp(vr_string(o,"to_node_id"),to) ||
        !*vr_string(o,"ticket_id") || !*vr_string(o,"client_entry_id") ||
        vr_timestamp(vr_string(o,"issued_at"),&issued) || vr_timestamp(vr_string(o,"expires_at"),&expiry) ||
        issued>now+300 || expiry<=now || expiry<=issued)goto done;
    *expires=expiry;rc=0;
done:json_object_put(o);return rc;
}
