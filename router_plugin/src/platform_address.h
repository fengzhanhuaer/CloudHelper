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
    char buf[512],*save=NULL;const char *args[48];size_t n=0;int firewall=!strncmp(text,"iptables ",9)||!strncmp(text,"ip6tables ",10);
    vr_copy(buf,sizeof(buf),text);
    for(char *word=strtok_r(buf," ",&save);word;word=strtok_r(NULL," ",&save)) {
        if(n>=44)return -1;
        if(!strcmp(word,"@")) {
            args[n++]=firewall?(add==2?"-C":add?"-I":"-D"):(add?"add":"del");
            if(firewall&&add==1) {
                word=strtok_r(NULL," ",&save);if(!word)return -1;
                int append=!strcmp(word,"CHVR_TLS")&&strstr(text,"-j DNAT ");
                if(append)args[n-1]="-A";
                args[n++]=word;if(!append)args[n++]="1";
            }
        } else args[n++]=word;
    }
    args[n]=NULL;return add==2?command_code(args,NULL,0):command(args,NULL,0);
}

/* A failed check is not evidence of a firewall reset. Query before inserting:
 * exit 1 means absent; lock, syntax, fork and timeout errors defer the repair.
 * Never flush chains, conntrack, policy routes or any established socket. */
static int firewall_chain(const char *table,const char *chain)
{
    int code=command_code((const char *const[]){"iptables","-t",table,"-n","-L",chain,NULL},NULL,0);
    if(!code)return 0;
    return code==1?RUN("iptables","-t",table,"-N",chain):-1;
}

static char carrier6_lan[16];
static int carrier6_port;
int vr_platform_carrier6(const vr_config *c,const vr_addresses *a)
{
    /* Only locally terminated TLS carriers. Never enable IPv6 forwarding,
       NAT, TUN interception, or alter the firmware's default policy. */
    int code=command_code((const char *const[]){"ip6tables","-t","filter","-n","-L","CHVR6_IN",NULL},NULL,0);
    if(code&&(code!=1||RUN("ip6tables","-t","filter","-N","CHVR6_IN")))return -1;
    char rules[5][512];
    snprintf(rules[0],sizeof(rules[0]),"ip6tables -t filter @ CHVR6_IN -p tcp --dport %d -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",c->port);
    snprintf(rules[1],sizeof(rules[1]),"ip6tables -t filter @ CHVR6_IN -i lo -p tcp --dport %d -j ACCEPT",c->port);
    snprintf(rules[2],sizeof(rules[2]),"ip6tables -t filter @ CHVR6_IN -i %s -p tcp --dport %d -j ACCEPT",a->lan_if,c->port);
    snprintf(rules[3],sizeof(rules[3]),"ip6tables -t filter @ CHVR6_IN -p tcp --dport %d -j %s",c->port,c->allow_wan?"ACCEPT":"DROP");
    strcpy(rules[4],"ip6tables -t filter @ INPUT -j CHVR6_IN");
    /* Insert accepts ahead of the guard, and expose the hook last. */
    const int order[]={3,2,1,0};
    for(size_t i=0;i<4;i++){const char *r=rules[order[i]];code=address_rule_run(r,2);if(code&&(code!=1||address_rule_run(r,1)))return -1;}
    /* A crash can leave the previous WAN permission in our chain. Remove
       its opposite guard only after the desired rules have been installed. */
    char opposite[512];snprintf(opposite,sizeof(opposite),"ip6tables -t filter @ CHVR6_IN -p tcp --dport %d -j %s",c->port,c->allow_wan?"DROP":"ACCEPT");
    code=address_rule_run(opposite,2);if(code==0){if(address_rule_run(opposite,0))return -1;}else if(code!=1)return -1;
    code=address_rule_run(rules[4],2);if(code&&(code!=1||address_rule_run(rules[4],1)))return -1;
    if(*carrier6_lan&&strcmp(carrier6_lan,a->lan_if)) {
        char old[512];snprintf(old,sizeof(old),"ip6tables -t filter @ CHVR6_IN -i %s -p tcp --dport %d -j ACCEPT",carrier6_lan,carrier6_port);
        if(address_rule_run(old,0))return -1;
    }
    vr_copy(carrier6_lan,sizeof(carrier6_lan),a->lan_if);carrier6_port=c->port;return 0;
}
void vr_platform_carrier6_stop(void)
{
    if(!carrier6_port)return;
    RUN("ip6tables","-D","INPUT","-j","CHVR6_IN");
    RUN("ip6tables","-F","CHVR6_IN");RUN("ip6tables","-X","CHVR6_IN");
    *carrier6_lan=0;carrier6_port=0;
}
int vr_platform_repair(const vr_config *c,const vr_addresses *a,const char *cidr)
{
    address_plan *p=calloc(1,sizeof(*p));int result=-1;
    if(!p)return -1;
#define RULE(...) do{if(address_rule(p,__VA_ARGS__))goto done;}while(0)
    if(firewall_chain("filter","CHVR_IN")||firewall_chain("filter","CHVR_FWD")||firewall_chain("nat","CHVR_NAT"))goto done;
    if(carrier_skiplog_port)RULE("iptables -t filter @ CHVR_IN -p tcp --dport %d -j SKIPLOG",carrier_skiplog_port);
    RULE("iptables -t filter @ CHVR_IN -p tcp --dport %d -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",c->port);
    RULE("iptables -t filter @ CHVR_IN -p tcp --dport 1054 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT");
    if(address_plan_build(p,c,a,cidr))goto done;
    if(c->enabled) {
        if(firewall_chain("mangle","CHVR_FLOW")||firewall_chain("mangle","CHVR_MARK"))goto done;
        RULE("iptables -t mangle @ CHVR_FLOW -m connmark --mark 0x43000000/0xff000000 -j CONNMARK --restore-mark --nfmask 0xff000000 --ctmask 0xff000000");
        RULE("iptables -t mangle @ CHVR_MARK -d %s -j RETURN",cidr);
        RULE("iptables -t mangle @ CHVR_MARK -p tcp -j CONNMARK --set-xmark 0x43000000/0xff000000");
        RULE("iptables -t mangle @ PREROUTING -i chvr0 -j CHVR_FLOW");
        if(tls_prepared) {
            if(firewall_chain("nat","CHVR_TLS"))goto done;
            const char *exclude[]={cidr,"0.0.0.0/8","10.0.0.0/8","100.64.0.0/10","127.0.0.0/8","169.254.0.0/16","172.16.0.0/12","192.168.0.0/16","224.0.0.0/3"};
            for(size_t i=0;i<sizeof(exclude)/sizeof(*exclude);i++)RULE("iptables -t nat @ CHVR_TLS -d %s -j RETURN",exclude[i]);
        }
    }
    RULE("iptables -t filter @ INPUT -j CHVR_IN");
    RULE("iptables -t filter @ FORWARD -j CHVR_FWD");
    RULE("iptables -t nat @ POSTROUTING -j CHVR_NAT");
    unsigned char missing[ADDRESS_RULE_MAX]={0};
    for(size_t i=0;i<p->count;i++) {
        if(strncmp(p->rule[i],"iptables ",9))continue;
        int code=address_rule_run(p->rule[i],2);
        if(code!=0&&code!=1)goto done;
        missing[i]=code==1;
    }
    /* Populate owned chains before exposing their global hooks. Reverse
       insertion keeps ACCEPTs before guards and RETURNs before catchalls. */
    for(int hooks=0;hooks<2;hooks++)for(size_t n=p->count;n;n--) {
        size_t i=n-1;
        int hook=strstr(p->rule[i]," @ INPUT ")||strstr(p->rule[i]," @ FORWARD ")||
            strstr(p->rule[i]," @ PREROUTING ")||strstr(p->rule[i]," @ POSTROUTING ");
        if(missing[i]&&hook==hooks&&address_rule_run(p->rule[i],1))goto done;
    }
    result=0;
done:
#undef RULE
    free(p);return result;
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
