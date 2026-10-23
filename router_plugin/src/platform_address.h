/* Included by platform.c: bounded address-dependent rule plans, no shell. */
#include <stdarg.h>
#define ADDRESS_RULE_MAX 128
typedef struct {size_t count;char rule[ADDRESS_RULE_MAX][512];} address_plan;
static int address_rule(address_plan *p,const char *format,...) __attribute__((format(printf,2,3)));
static int address_rule(address_plan *p,const char *format,...)
{
    if(p->count==ADDRESS_RULE_MAX)return -1;
    va_list ap;va_start(ap,format);int n=vsnprintf(p->rule[p->count],512,format,ap);va_end(ap);
    if(n<0||n>=512)return -1;
    p->count++;return 0;
}
static int address_plan_build(address_plan *p,const vr_config *c,const vr_addresses *a,const char *cidr)
{
#define RULE(...) do{if(address_rule(p,__VA_ARGS__))return -1;}while(0)
    if(c->gateway.enabled&&vr_gateway_contains(&c->gateway,a->lan))
        RULE("iptables -t filter @ CHVR_IN -i chvr0 -s %s -d %s -j ACCEPT",cidr,a->lan);
    RULE("iptables -t filter @ CHVR_IN -i %s -d %s -p tcp --dport %d -j ACCEPT",a->lan_if,a->lan,c->port);
    RULE("iptables -t filter @ CHVR_IN ! -i %s -d %s -p tcp --dport %d -m conntrack --ctstate NEW -j DROP",a->lan_if,a->lan,c->port);
    RULE("iptables -t filter @ CHVR_FWD -i %s -o chvr0 -d %s -j ACCEPT",a->lan_if,cidr);
    RULE("iptables -t filter @ CHVR_FWD -i chvr0 -o %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",a->lan_if);
    if(*a->wan_if) {
        RULE("iptables -t filter @ CHVR_FWD -i chvr0 -o %s -s %s -j ACCEPT",a->wan_if,cidr);
        RULE("iptables -t filter @ CHVR_FWD -i %s -o chvr0 -d %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",a->wan_if,cidr);
        RULE("iptables -t nat @ CHVR_NAT -s %s -o %s -j MASQUERADE",cidr,a->wan_if);
    }
    if(c->gateway.enabled)for(size_t i=0;i<c->gateway.cidr_count;i++) {
        RULE("iptables -t filter @ CHVR_FWD -i chvr0 -o %s -s %s -d %s -j ACCEPT",a->lan_if,cidr,c->gateway.cidrs[i]);
        RULE("iptables -t nat @ CHVR_NAT -o %s -s %s -d %s -j MASQUERADE",a->lan_if,cidr,c->gateway.cidrs[i]);
    }
    if(c->allow_wan&&*a->wan&&*a->wan_if)
        RULE("iptables -t filter @ CHVR_IN -i %s -d %s -p tcp --dport %d -j ACCEPT",a->wan_if,a->wan,c->port);
    if(c->enabled) {
        RULE("ip rule @ priority 18601 iif %s lookup 187",a->lan_if);
        RULE("ip rule @ priority 18600 iif %s fwmark 0x43000000/0xff000000 lookup 188",a->lan_if);
        RULE("iptables -t mangle @ PREROUTING -i %s -j CHVR_FLOW",a->lan_if);
        RULE("iptables -t mangle @ FORWARD -i %s -o chvr0 -j CHVR_MARK",a->lan_if);
        RULE("iptables -t filter @ CHVR_FWD -i %s -o chvr0 -j ACCEPT",a->lan_if);
        /* A disabled sniffer retains its DNAT accepts for established flows. */
        if(tls_prepared) {
            RULE("iptables -t filter @ CHVR_IN -i %s -d %s -p tcp --dport 1054 -m conntrack --ctstate DNAT -j ACCEPT",a->lan_if,a->lan);
            RULE("iptables -t nat @ CHVR_TLS -p tcp --dport 443 -j DNAT --to-destination %s:1054",a->lan);
        }
        if(tls_active)RULE("iptables -t nat @ PREROUTING -i %s -p tcp --dport 443 -j CHVR_TLS",a->lan_if);
    }
#undef RULE
    return 0;
}
static int address_rule_present(const address_plan *p,const char *rule)
{for(size_t i=0;i<p->count;i++)if(!strcmp(p->rule[i],rule))return 1;return 0;}
static int address_rule_run(const char *text,int add)
{
    char buf[512],*save=NULL;const char *args[48];size_t n=0;int firewall=!strncmp(text,"iptables ",9);
    vr_copy(buf,sizeof(buf),text);
    for(char *word=strtok_r(buf," ",&save);word;word=strtok_r(NULL," ",&save)) {
        if(n>=44)return -1;
        if(!strcmp(word,"@")) {
            args[n++]=firewall?(add?"-I":"-D"):(add?"add":"del");
            if(firewall&&add) {
                word=strtok_r(NULL," ",&save);if(!word)return -1;
                if(!strcmp(word,"CHVR_TLS"))args[n-1]="-A";
                args[n++]=word;if(strcmp(word,"CHVR_TLS"))args[n++]="1";
            }
        } else args[n++]=word;
    }
    args[n]=NULL;return command(args,NULL,0);
}
int vr_platform_readdress(const vr_config *c,const vr_addresses *old,const vr_addresses *next,const char *cidr)
{
    address_plan *before=calloc(1,sizeof(*before)),*after=calloc(1,sizeof(*after));int result=-1;
    if(!before||!after)goto done;
    if(address_plan_build(before,c,old,cidr)||address_plan_build(after,c,next,cidr))goto done;
    /* Prepend in reverse plan order: new accepts precede old address guards.
       Never flush chains, conntrack, TUN routes or live sockets. */
    size_t added=after->count,removed=0;
    while(added) {
        size_t i=added-1;
        if(!address_rule_present(before,after->rule[i])&&address_rule_run(after->rule[i],1))goto rollback;
        added=i;
    }
    for(;removed<before->count;removed++)if(!address_rule_present(after,before->rule[removed])&&address_rule_run(before->rule[removed],0))goto rollback;
    vr_copy(policy_lan_if,sizeof(policy_lan_if),next->lan_if);result=0;goto done;
rollback:
    while(removed){size_t i=--removed;if(!address_rule_present(after,before->rule[i]))address_rule_run(before->rule[i],1);}
    for(size_t i=added;i<after->count;i++)if(!address_rule_present(before,after->rule[i]))address_rule_run(after->rule[i],0);
done:
    free(before);free(after);return result;
}
