#define _GNU_SOURCE
#include "platform.h"
#include "transport.h"
#include <arpa/inet.h>
#include <errno.h>
#include <dirent.h>
#include <fcntl.h>
#include <ifaddrs.h>
#include <linux/if_tun.h>
#include <net/if.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mount.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

/* BE86U's BusyBox mount applet rejects /usr and /www targets. These fixed,
 * root-only operations use the same kernel API, with no arbitrary mount path
 * or remote API. Never remove another component's mount. */
static int runtime_mounted(const char *target,const char *source)
{
    FILE *f=fopen("/proc/self/mounts","r");if(!f)return -1;
    char line[2048],src[512],dst[512];int found=0;
    while(fgets(line,sizeof(line),f))if(sscanf(line,"%511s %511s",src,dst)==2 && !strcmp(dst,target)) {
        found=source&&strcmp(src,source)?-1:1;break;
    }
    fclose(f);return found;
}
static int runtime_marker(const char *path,const char *marker)
{
    struct stat st;if(lstat(path,&st) || !S_ISREG(st.st_mode) || st.st_uid!=0 || st.st_mode&0022)return 0;
    FILE *f=fopen(path,"r");if(!f)return 0;
    char line[1024];int found=0;size_t used=0;
    while(used<65536 && fgets(line,sizeof(line),f)) {
        used+=strlen(line);if(strstr(line,marker)){found=1;break;}
    }
    fclose(f);return found;
}
int vr_runtime_mount(const char *op)
{
    const char *local="/usr/local",*menu="/www/require/modules/menuTree.js",*web="/usr/sbin/httpd";
    const char *menu_source="/tmp/cloudhelper-router.menu.js",*web_source="/usr/local/cloudhelper-router/httpd-launch";
    if(geteuid()!=0)return -1;
    if(!strcmp(op,"local")) {
        if(runtime_mounted(local,NULL)!=0)return -1;
        char link[64];ssize_t n=readlink("/usr/local/share",link,sizeof(link));
        if(n!=10 || memcmp(link,"/tmp/share",10))return -1;
        DIR *dir=opendir(local);if(!dir)return -1;
        struct dirent *entry;int valid=1,shares=0;
        while((entry=readdir(dir))) {
            if(!strcmp(entry->d_name,".") || !strcmp(entry->d_name,".."))continue;
            if(strcmp(entry->d_name,"share"))valid=0;else shares++;
        }
        closedir(dir);if(!valid || shares!=1)return -1;
        return mount("chvr-local",local,"tmpfs",MS_NODEV|MS_NOSUID,"size=2m,mode=755");
    }
    if(!strcmp(op,"unlocal")) {
        struct stat st;
        if(runtime_mounted(local,"chvr-local")!=1 || lstat("/usr/local/cloudhelper-router/.owned",&st) || !S_ISREG(st.st_mode) || st.st_uid)return -1;
        return umount(local);
    }
    const char *target=NULL,*source=NULL,*marker=NULL;int remove=0;
    if(!strcmp(op,"menu") || !strcmp(op,"unmenu")) {
        target=menu;source=menu_source;marker="CHVR_JFFS_MENU";remove=!strcmp(op,"unmenu");
    } else if(!strcmp(op,"web") || !strcmp(op,"unweb")) {
        target=web;source=web_source;marker="CHVR_HTTPD_WRAPPER";remove=!strcmp(op,"unweb");
    } else return -1;
    if(remove) {
        if(runtime_mounted(target,NULL)!=1 || !runtime_marker(target,marker))return -1;
        return umount(target);
    }
    if(runtime_mounted(target,NULL)!=0 || !runtime_marker(source,marker))return -1;
    struct stat st;if(lstat(target,&st) || !S_ISREG(st.st_mode))return -1;
    return mount(source,target,NULL,MS_BIND,NULL);
}

static void command_output(int fd,char *out,size_t capacity,size_t *used)
{
    char buf[512];ssize_t n;
    for(;;) {
        n=read(fd,buf,sizeof(buf));
        if(n<0&&errno==EINTR)continue;
        if(n<=0)break;
        if(out&&capacity) {
            size_t k=(size_t)n;if(k>capacity-1-*used)k=capacity-1-*used;
            memcpy(out+*used,buf,k);*used+=k;
        }
    }
}
static int command(const char *const *args,char *out,size_t capacity)
{
    int pipes[2];if(pipe2(pipes,O_CLOEXEC|O_NONBLOCK))return -1;
    pid_t pid=fork();if(pid<0){close(pipes[0]);close(pipes[1]);return -1;}
    if(pid==0) {
        close(pipes[0]);fcntl(pipes[1],F_SETFL,0);dup2(pipes[1],STDOUT_FILENO);close(pipes[1]);
        int null=open("/dev/null",O_WRONLY);if(null>=0){dup2(null,STDERR_FILENO);close(null);}
        execvp(args[0],(char *const *)args);_exit(127);
    }
    close(pipes[1]);size_t used=0;int status=-1,done=0;uint64_t deadline=vr_millis()+5000;
    while(vr_millis()<deadline) {
        command_output(pipes[0],out,capacity,&used);
        if(waitpid(pid,&status,WNOHANG)==pid){done=1;break;}
        struct pollfd p={pipes[0],POLLIN,0};poll(&p,1,20);
    }
    if(!done){kill(pid,SIGKILL);while(waitpid(pid,&status,0)<0&&errno==EINTR){}}
    /* A child may write and exit between the empty read and waitpid. Reaping
       it does not consume its pipe: retain that final output before closing. */
    command_output(pipes[0],out,capacity,&used);
    close(pipes[0]);if(out&&capacity){while(used&&(out[used-1]=='\n'||out[used-1]=='\r'))used--;out[used]=0;}
    return done&&WIFEXITED(status)&&WEXITSTATUS(status)==0?0:-1;
}

#define RUN(...) command((const char *const[]){__VA_ARGS__,NULL},NULL,0)
static int nvram(const char *key,char *out,size_t n){return command((const char *const[]){"nvram","get",key,NULL},out,n);}
static int ipv4(const char *s){struct in_addr a;return inet_pton(AF_INET,s,&a)==1&&a.s_addr&&ntohl(a.s_addr)<0xe0000000u;}
static int iface(const char *s){if(!*s)return 0;for(;*s;s++)if(!((*s>='a'&&*s<='z')||(*s>='A'&&*s<='Z')||(*s>='0'&&*s<='9')||*s=='_'||*s=='-'||*s=='.'))return 0;return 1;}

static int address_field(const char *key,char *out,size_t n,char *detail,size_t capacity)
{
    if(!nvram(key,out,n))return 0;
    snprintf(detail,capacity,"NVRAM read failed: %s; bindings retained",key);return -1;
}
static int addresses_read(vr_addresses *out,char *detail,size_t capacity)
{
    vr_addresses a={0};char unit[8]={0},key[48];
    if(address_field("lan_ipaddr",a.lan,sizeof(a.lan),detail,capacity) ||
       address_field("lan_ifname",a.lan_if,sizeof(a.lan_if),detail,capacity))return -1;
    if(!ipv4(a.lan)||!iface(a.lan_if)) {
        snprintf(detail,capacity,"Invalid NVRAM field: %s; bindings retained",!ipv4(a.lan)?"lan_ipaddr":"lan_ifname");return -1;
    }
    if(address_field("wan_primary",unit,sizeof(unit),detail,capacity))return -1;
    if(*unit&&strcmp(unit,"0")&&strcmp(unit,"1")) {
        snprintf(detail,capacity,"Invalid NVRAM field: wan_primary; bindings retained");return -1;
    }
    int primary=!strcmp(unit,"1")?1:0;
    snprintf(key,sizeof(key),"wan%d_ipaddr",primary);
    if(address_field(key,a.wan,sizeof(a.wan),detail,capacity))return -1;
    if(!ipv4(a.wan)) {
        if(*a.wan&&strcmp(a.wan,"0.0.0.0")) {
            snprintf(detail,capacity,"Invalid NVRAM field: %s; bindings retained",key);return -1;
        }
        memset(a.wan,0,sizeof(a.wan));
    }
    snprintf(key,sizeof(key),"wan%d_gw_ifname",primary);
    if(address_field(key,a.wan_if,sizeof(a.wan_if),detail,capacity))return -1;
    if(!*a.wan_if) {
        snprintf(key,sizeof(key),"wan%d_ifname",primary);
        if(address_field(key,a.wan_if,sizeof(a.wan_if),detail,capacity))return -1;
    }
    if(*a.wan_if&&!iface(a.wan_if)) {
        snprintf(detail,capacity,"Invalid NVRAM field: %s; bindings retained",key);return -1;
    }
    *out=a;
    return 0;
}
int vr_addresses_get(vr_addresses *a)
{char detail[128];return addresses_read(a,detail,sizeof(detail));}

static int addresses_equal(const vr_addresses *a,const vr_addresses *b)
{return !strcmp(a->lan,b->lan)&&!strcmp(a->wan,b->wan)&&!strcmp(a->lan_if,b->lan_if)&&!strcmp(a->wan_if,b->wan_if);}

int vr_addresses_check(vr_address_watch *watch,const vr_addresses *current,vr_addresses *next,char *detail,size_t capacity)
{
    vr_addresses a;
    if(capacity)*detail=0;
    if(addresses_read(&a,detail,capacity)){watch->pending=0;return -1;}
    if(addresses_equal(&a,current)){watch->pending=0;return 0;}
    int confirmed=watch->pending&&addresses_equal(&a,&watch->candidate);
    watch->candidate=a;watch->pending=1;*next=a;
    snprintf(detail,capacity,"LAN/WAN %s: LAN %s/%s -> %s/%s; WAN %s/%s -> %s/%s",
        confirmed?"change confirmed":"change pending",current->lan,current->lan_if,a.lan,a.lan_if,
        *current->wan?current->wan:"-",*current->wan_if?current->wan_if:"-",*a.wan?a.wan:"-",*a.wan_if?a.wan_if:"-");
    return confirmed?2:1;
}

int vr_bind4(const char *ip,int port,int tcp)
{
    struct sockaddr_in sa={0};sa.sin_family=AF_INET;sa.sin_port=htons((uint16_t)port);
    if(!ipv4(ip)||port<1||port>65535||inet_pton(AF_INET,ip,&sa.sin_addr)!=1)return -1;
    int fd=socket(AF_INET,(tcp?SOCK_STREAM:SOCK_DGRAM)|SOCK_CLOEXEC|SOCK_NONBLOCK,0);if(fd<0)return -1;
    int yes=1;setsockopt(fd,SOL_SOCKET,SO_REUSEADDR,&yes,sizeof(yes));
    if(bind(fd,(struct sockaddr *)&sa,sizeof(sa))||(tcp&&listen(fd,8))){close(fd);return -1;}return fd;
}

static int carrier_skiplog_port;
static char policy_lan_if[IFNAMSIZ];
static int tls_active;
static int tls_prepared;
#include "platform_address.h"
static int carrier_skiplog_available(void)
{
    FILE *f=fopen("/proc/net/ip_tables_targets","r");if(!f)return 0;
    char name[64];int found=0;
    while(fgets(name,sizeof(name),f))if(!strcmp(name,"SKIPLOG\n")){found=1;break;}
    fclose(f);return found;
}

int vr_platform_tls(const vr_addresses *a,const char *cidr,int enabled)
{
    if(!enabled) {
        if(!RUN("iptables","-t","nat","-C","PREROUTING","-i",a->lan_if,"-p","tcp","--dport","443","-j","CHVR_TLS")&&
            RUN("iptables","-t","nat","-D","PREROUTING","-i",a->lan_if,"-p","tcp","--dport","443","-j","CHVR_TLS"))return -1;
        tls_active=0;return 0;
    }
    RUN("iptables","-t","nat","-N","CHVR_TLS");
    if(RUN("iptables","-t","nat","-F","CHVR_TLS"))return -1;
    const char *exclude[]={cidr,"0.0.0.0/8","10.0.0.0/8","100.64.0.0/10","127.0.0.0/8","169.254.0.0/16","172.16.0.0/12","192.168.0.0/16","224.0.0.0/3"};
    for(size_t i=0;i<sizeof(exclude)/sizeof(*exclude);i++)if(RUN("iptables","-t","nat","-A","CHVR_TLS","-d",exclude[i],"-j","RETURN"))return -1;
    char target[32];snprintf(target,sizeof(target),"%s:1054",a->lan);
    if(RUN("iptables","-t","nat","-A","CHVR_TLS","-p","tcp","--dport","443","-j","DNAT","--to-destination",target))return -1;
    if(RUN("iptables","-C","CHVR_IN","-i",a->lan_if,"-d",a->lan,"-p","tcp","--dport","1054","-m","conntrack","--ctstate","DNAT","-j","ACCEPT")&&
        RUN("iptables","-A","CHVR_IN","-i",a->lan_if,"-d",a->lan,"-p","tcp","--dport","1054","-m","conntrack","--ctstate","DNAT","-j","ACCEPT"))return -1;
    if(RUN("iptables","-t","nat","-C","PREROUTING","-i",a->lan_if,"-p","tcp","--dport","443","-j","CHVR_TLS")&&
        RUN("iptables","-t","nat","-I","PREROUTING","1","-i",a->lan_if,"-p","tcp","--dport","443","-j","CHVR_TLS"))return -1;
    tls_prepared=tls_active=1;return 0;
}

int vr_platform_start(const vr_config *c,const vr_addresses *a,const char *ip,const char *cidr)
{
    vr_copy(policy_lan_if,sizeof(policy_lan_if),a->lan_if);
    int fd=open("/dev/net/tun",O_RDWR|O_CLOEXEC|O_NONBLOCK);
    if(fd<0){RUN("modprobe","tun");fd=open("/dev/net/tun",O_RDWR|O_CLOEXEC|O_NONBLOCK);}
    if(fd<0)return -1;
    struct ifreq req={0};strcpy(req.ifr_name,"chvr0");req.ifr_flags=IFF_TUN|IFF_NO_PI;
    if(ioctl(fd,TUNSETIFF,&req))goto fail;
    char address[32],port[8];snprintf(address,sizeof(address),"%s/32",ip);snprintf(port,sizeof(port),"%d",c->port);
    /* Do not assign the virtual address to the interface: that would create
       a global local route and consume WAN NAT replies before they reach TUN.
       Only packets injected by this service may reach the node's local IP. */
    if(RUN("ip","link","set","dev","chvr0","mtu","1400","up") ||
       RUN("ip","route","add",cidr,"dev","chvr0","proto","186") ||
       RUN("ip","route","add","local",address,"dev","chvr0","table","186","proto","186") ||
       RUN("ip","rule","add","priority","18600","iif","chvr0","lookup","186"))goto fail;
    RUN("iptables","-N","CHVR_IN");RUN("iptables","-N","CHVR_FWD");RUN("iptables","-t","nat","-N","CHVR_NAT");
    if(RUN("iptables","-F","CHVR_IN") || RUN("iptables","-F","CHVR_FWD") || RUN("iptables","-t","nat","-F","CHVR_NAT"))goto fail;
    carrier_skiplog_port=0;
    /* Broadcom BLOG acceleration can corrupt a locally terminated TLS flow
       after its handshake. Keep only this service's inbound carriers on the
       kernel path; SKIPLOG continues normal authentication/firewall checks. */
    if(carrier_skiplog_available()) {
        if(RUN("iptables","-A","CHVR_IN","-p","tcp","--dport",port,"-j","SKIPLOG"))goto fail;
        carrier_skiplog_port=c->port;
    }
    /* Address/interface refresh must not reject previously authenticated
       streams merely because their original listener is no longer current. */
    if(RUN("iptables","-A","CHVR_IN","-p","tcp","--dport",port,"-m","conntrack","--ctstate","ESTABLISHED,RELATED","-j","ACCEPT") ||
       RUN("iptables","-A","CHVR_IN","-p","tcp","--dport","1054","-m","conntrack","--ctstate","ESTABLISHED,RELATED","-j","ACCEPT"))goto fail;
    /* vr_incoming authenticates the original node and checks the gateway CIDR
       before injecting traffic. The router's own LAN address uses INPUT,
       unlike other LAN hosts; place this before the carrier-port guard. */
    if(c->gateway.enabled&&vr_gateway_contains(&c->gateway,a->lan)&&
       RUN("iptables","-A","CHVR_IN","-i","chvr0","-s",cidr,"-d",a->lan,"-j","ACCEPT"))goto fail;
    if(RUN("iptables","-A","CHVR_IN","-i",a->lan_if,"-d",a->lan,"-p","tcp","--dport",port,"-j","ACCEPT") ||
       RUN("iptables","-A","CHVR_IN","!","-i",a->lan_if,"-d",a->lan,"-p","tcp","--dport",port,"-m","conntrack","--ctstate","NEW","-j","DROP") ||
       RUN("iptables","-A","CHVR_FWD","-i",a->lan_if,"-o","chvr0","-d",cidr,"-j","ACCEPT") ||
       RUN("iptables","-A","CHVR_FWD","-i","chvr0","-o",a->lan_if,"-m","conntrack","--ctstate","ESTABLISHED,RELATED","-j","ACCEPT"))goto fail;
    if(*a->wan_if) {
        if(RUN("iptables","-A","CHVR_FWD","-i","chvr0","-o",a->wan_if,"-s",cidr,"-j","ACCEPT") ||
           RUN("iptables","-A","CHVR_FWD","-i",a->wan_if,"-o","chvr0","-d",cidr,"-m","conntrack","--ctstate","ESTABLISHED,RELATED","-j","ACCEPT") ||
           RUN("iptables","-t","nat","-A","CHVR_NAT","-s",cidr,"-o",a->wan_if,"-j","MASQUERADE"))goto fail;
    }
    if(c->gateway.enabled)for(size_t i=0;i<c->gateway.cidr_count;i++) {
        const char *dest=c->gateway.cidrs[i];
        if(RUN("iptables","-A","CHVR_FWD","-i","chvr0","-o",a->lan_if,"-s",cidr,"-d",dest,"-j","ACCEPT") ||
           RUN("iptables","-t","nat","-A","CHVR_NAT","-o",a->lan_if,"-s",cidr,"-d",dest,"-j","MASQUERADE"))goto fail;
    }
    if(c->allow_wan&&*a->wan&&*a->wan_if&&RUN("iptables","-A","CHVR_IN","-i",a->wan_if,"-d",a->wan,"-p","tcp","--dport",port,"-j","ACCEPT"))goto fail;
    if(RUN("iptables","-C","INPUT","-j","CHVR_IN") && RUN("iptables","-I","INPUT","1","-j","CHVR_IN"))goto fail;
    if(RUN("iptables","-C","FORWARD","-j","CHVR_FWD") && RUN("iptables","-I","FORWARD","1","-j","CHVR_FWD"))goto fail;
    if(RUN("iptables","-t","nat","-C","POSTROUTING","-j","CHVR_NAT") && RUN("iptables","-t","nat","-I","POSTROUTING","1","-j","CHVR_NAT"))goto fail;
    if(c->enabled) {
        /* LAN only: OUTPUT and TUN reinjection never enter this table or the
           sniff listener. Carriers and controller sockets keep their path. */
        if(RUN("ip","rule","add","priority","18601","iif",a->lan_if,"lookup","187"))goto fail;
        /* Keep already captured TCP flows entering TUN when their CIDR rule
           is removed. Only LAN conntracks that actually crossed chvr0 receive
           our reserved high-byte mark; preserve all lower mark bits. */
        if(RUN("ip","route","add","default","dev","chvr0","table","188","proto","186")||
           RUN("ip","rule","add","priority","18600","iif",a->lan_if,"fwmark","0x43000000/0xff000000","lookup","188"))goto fail;
        /* Reverse validation of an injected reply must use the same sticky
           route as its established LAN conntrack, even after CIDR removal.
           Keep system and physical-interface rp_filter settings unchanged. */
        int vmark=open("/proc/sys/net/ipv4/conf/chvr0/src_valid_mark",O_WRONLY|O_CLOEXEC);
        if(vmark<0)goto fail;
        ssize_t marked=write(vmark,"1\n",2);close(vmark);if(marked!=2)goto fail;
        RUN("iptables","-t","mangle","-N","CHVR_FLOW");RUN("iptables","-t","mangle","-N","CHVR_MARK");
        if(RUN("iptables","-t","mangle","-F","CHVR_FLOW")||RUN("iptables","-t","mangle","-F","CHVR_MARK")||
           RUN("iptables","-t","mangle","-A","CHVR_FLOW","-m","connmark","--mark","0x43000000/0xff000000","-j","CONNMARK","--restore-mark","--nfmask","0xff000000","--ctmask","0xff000000")||
           RUN("iptables","-t","mangle","-A","CHVR_MARK","-d",cidr,"-j","RETURN")||
           RUN("iptables","-t","mangle","-A","CHVR_MARK","-p","tcp","-j","CONNMARK","--set-xmark","0x43000000/0xff000000")||
           RUN("iptables","-t","mangle","-I","PREROUTING","1","-i",a->lan_if,"-j","CHVR_FLOW")||
           RUN("iptables","-t","mangle","-I","PREROUTING","1","-i","chvr0","-j","CHVR_FLOW")||
           RUN("iptables","-t","mangle","-I","FORWARD","1","-i",a->lan_if,"-o","chvr0","-j","CHVR_MARK"))goto fail;
        struct ifaddrs *interfaces=NULL;if(getifaddrs(&interfaces))goto fail;
        int failed=0;
        for(struct ifaddrs *p=interfaces;p;p=p->ifa_next) {
            if(!p->ifa_addr||p->ifa_addr->sa_family!=AF_INET||!p->ifa_netmask||!strcmp(p->ifa_name,"chvr0"))continue;
            struct sockaddr_in *ip4=(struct sockaddr_in *)p->ifa_addr,*mask=(struct sockaddr_in *)p->ifa_netmask;
            struct in_addr base={ip4->sin_addr.s_addr&mask->sin_addr.s_addr};char net[32],text[16];unsigned bits=0;
            for(uint32_t m=ntohl(mask->sin_addr.s_addr);m&0x80000000u;m<<=1)bits++;
            if(!bits)continue;
            inet_ntop(AF_INET,&base,text,sizeof(text));snprintf(net,sizeof(net),"%s/%u",text,bits);
            if(RUN("ip","route","replace","throw",net,"table","187","proto","186"))failed=1;
        }
        freeifaddrs(interfaces);if(failed)goto fail;
        if(c->tls_sni_enabled&&vr_platform_tls(a,cidr,1))goto fail;
        /* Real-IP CIDRs are forwarded into TUN without using the Fake IP CIDR. */
        if(RUN("iptables","-A","CHVR_FWD","-i",a->lan_if,"-o","chvr0","-j","ACCEPT"))goto fail;
    }
    return fd;
fail:close(fd);vr_platform_stop(cidr);return -1;
}


/* Preserve connected physical LANs when a remote owner publishes an overlapping
   network. A conflicting CIDR is not installed over the management connection. */
static int connected_cidr(const char *cidr)
{
    struct ifaddrs *list=NULL;if(getifaddrs(&list))return 1;int found=0;
    for(struct ifaddrs *p=list;p;p=p->ifa_next) {
        if(!p->ifa_addr||p->ifa_addr->sa_family!=AF_INET||!strcmp(p->ifa_name,"chvr0")||!p->ifa_netmask)continue;
        struct sockaddr_in *a=(struct sockaddr_in *)p->ifa_addr,*m=(struct sockaddr_in *)p->ifa_netmask;
        char ip[16];inet_ntop(AF_INET,&a->sin_addr,ip,sizeof(ip));
        if(vr_cidr_contains(cidr,ip)){found=1;break;}
        char base[64];vr_copy(base,sizeof(base),cidr);char *slash=strchr(base,'/');if(!slash)continue;*slash=0;struct in_addr target;
        if(inet_pton(AF_INET,base,&target)==1&&(a->sin_addr.s_addr&m->sin_addr.s_addr)==(target.s_addr&m->sin_addr.s_addr)){found=1;break;}
    }
    freeifaddrs(list);return found;
}
static int within_connected_cidr(const char *cidr)
{
    char text[64];vr_copy(text,sizeof(text),cidr);char *slash=strchr(text,'/');if(!slash)return 1;*slash++=0;
    unsigned bits=(unsigned)atoi(slash);struct in_addr base;if(inet_pton(AF_INET,text,&base)!=1)return 1;
    struct ifaddrs *list=NULL;if(getifaddrs(&list))return 1;int found=0;
    for(struct ifaddrs *p=list;p;p=p->ifa_next) {
        if(!p->ifa_addr||p->ifa_addr->sa_family!=AF_INET||!strcmp(p->ifa_name,"chvr0")||!p->ifa_netmask)continue;
        struct sockaddr_in *a=(struct sockaddr_in *)p->ifa_addr,*mask=(struct sockaddr_in *)p->ifa_netmask;
        unsigned local_bits=0;for(uint32_t m=ntohl(mask->sin_addr.s_addr);m&0x80000000u;m<<=1)local_bits++;
        if(bits>=local_bits&&(base.s_addr&mask->sin_addr.s_addr)==(a->sin_addr.s_addr&mask->sin_addr.s_addr)){found=1;break;}
    }
    freeifaddrs(list);return found;
}
typedef struct {char cidr[19];int table;} route_spec;
static int normalized_cidr(const char *raw,char out[19])
{
    char buf[64];if(vr_copy(buf,sizeof(buf),raw))return -1;
    char *slash=strchr(buf,'/'),*end;long bits=32;if(slash){*slash++=0;bits=strtol(slash,&end,10);if(!*slash||*end||bits<0||bits>32)return -1;}
    struct in_addr ip;if(inet_pton(AF_INET,buf,&ip)!=1)return -1;
    ip.s_addr=htonl(ntohl(ip.s_addr)&(bits?0xffffffffu<<(32-bits):0));
    char text[16];inet_ntop(AF_INET,&ip,text,sizeof(text));snprintf(out,19,"%s/%ld",text,bits);return 0;
}
static int published_routes(json_object *policy,route_spec seen[256],size_t *count)
{
    json_object *rules=vr_member(policy,"route_rules");size_t installed=0;
    if(!rules){*count=0;return 0;}
    if(!json_object_is_type(rules,json_type_array))return -1;
    for(size_t i=0;i<(size_t)json_object_array_length(rules);i++) {
        json_object *rule=json_object_array_get_idx(rules,i);const char *id=vr_string(rule,"id");
        json_object *enabled=vr_member(rule,"enabled");if(enabled&&!json_object_get_boolean(enabled))continue;
        int gateway=!strncmp(id,"gateway-proxy-",14)||!strncmp(id,"linux-router-",13);
        const char *action=vr_string(rule,"action");
        if(strcmp(action,"probe_exit")&&(gateway||strcmp(action,"reject")))continue;
        json_object *entries=vr_member(rule,"entries");
        for(size_t j=0;j<(size_t)json_object_array_length(entries);j++) {
            const char *entry=json_object_get_string(json_object_array_get_idx(entries,j));char cidr[19];
            if(!entry)continue;
            const char *value=NULL;
            if(!strncmp(entry,"cidr:",5))value=entry+5;
            else if(!strncmp(entry,"ip_cidr:",8)||!strncmp(entry,"ip-cidr:",8))value=entry+8;
            else if(!strncmp(entry,"ip:",3))value=entry+3;
            if(!value||(gateway?vr_private_cidr(value,cidr):normalized_cidr(value,cidr)))continue;
            if(gateway&&connected_cidr(cidr))continue;
            /* Connected destinations remain reachable via the main table.
               Broad rules are allowed; table 187 has connected throw routes. */
            if(!gateway&&within_connected_cidr(cidr))continue;
            int table=gateway?254:187;
            size_t k;for(k=0;k<installed;k++)if(!strcmp(cidr,seen[k].cidr)&&seen[k].table==table)break;
            if(k<installed)continue;
            if(installed>=256)return -1;
            strcpy(seen[installed].cidr,cidr);seen[installed++].table=table;
        }
    }
    *count=installed;return 0;
}

static int listed(route_spec list[256],size_t count,route_spec item)
{for(size_t i=0;i<count;i++)if(!strcmp(list[i].cidr,item.cidr)&&list[i].table==item.table)return 1;return 0;}
static int route_command(const char *op,route_spec item)
{
    if(item.table==187)return RUN("ip","route",op,item.cidr,"dev","chvr0","table","187","proto","186");
    return RUN("ip","route",op,item.cidr,"dev","chvr0","proto","186");
}

int vr_platform_routes_update(json_object *previous,json_object *next)
{
    route_spec old[256],current[256];size_t before=0,after=0,added=0,removed=0;
    if(published_routes(previous,old,&before)||published_routes(next,current,&after))return -1;
    for(;added<after;added++)if(!listed(old,before,current[added])&&
        route_command("add",current[added]))goto rollback;
    for(;removed<before;removed++)if(!listed(current,after,old[removed])&&
        route_command("del",old[removed]))goto rollback;
    return 0;
rollback:
    for(size_t i=0;i<removed;i++)if(!listed(current,after,old[i]))route_command("add",old[i]);
    for(size_t i=0;i<added;i++)if(!listed(old,before,current[i]))route_command("del",current[i]);
    return -1;
}

int vr_platform_routes(json_object *policy)
{
    return vr_platform_routes_update(NULL,policy);
}

int vr_platform_dns(int enabled)
{
    const char *path="/tmp/resolv.dnsmasq",*backup="/tmp/cloudhelper-dns.original";
    const char *owned="# CloudHelper managed upstream\nserver=127.0.0.1#1053\n";
    char current[16384],original[16384];size_t n=0,saved=0;
    if(vr_read_file(path,current,sizeof(current),&n))return enabled?-1:0;
    int ours=n==strlen(owned)&&!memcmp(current,owned,n);
    if(enabled) {
        if(ours){struct stat st;if(stat(path,&st))return -1;
            if((st.st_mode&0777)==0644)return 0;
            if(chmod(path,0644))return -1;
            return RUN("killall","-HUP","dnsmasq");}
        /* Capture the latest vendor update (DHCP/WAN/DNS settings), only in RAM.
           The stock servers-file is reloaded by SIGHUP without restarting DHCP. */
        /* dnsmasq reloads after dropping to nobody; private atomic-write mode
           is appropriate for the backup, but not its public servers file. */
        if(vr_atomic_write(backup,current,n)||vr_atomic_write(path,owned,strlen(owned))||chmod(path,0644))return -1;
        return RUN("killall","-HUP","dnsmasq");
    }
    if(ours) {
        if(vr_read_file(backup,original,sizeof(original),&saved)||vr_atomic_write(path,original,saved)||chmod(path,0644))return -1;
        if(RUN("killall","-HUP","dnsmasq"))return -1;
    }
    /* If the vendor already replaced our file, keep its new contents. */
    if(unlink(backup)&&errno!=ENOENT)return -1;
    return 0;
}

void vr_platform_stop(const char *cidr)
{
    vr_platform_dns(0);
    (void)cidr;RUN("ip","route","flush","dev","chvr0","proto","186");
    RUN("ip","rule","del","priority","18600","iif","chvr0","lookup","186");
    RUN("ip","route","flush","table","186","dev","chvr0","proto","186");
    /* Only this plugin owns priority 18601 and protocol 186 in table 187. */
    RUN("ip","rule","del","priority","18601","lookup","187");
    RUN("ip","route","flush","table","187","proto","186");
    if(*policy_lan_if) {
        RUN("ip","rule","del","priority","18600","iif",policy_lan_if,"fwmark","0x43000000/0xff000000","lookup","188");
        RUN("iptables","-t","mangle","-D","PREROUTING","-i",policy_lan_if,"-j","CHVR_FLOW");
        RUN("iptables","-t","mangle","-D","FORWARD","-i",policy_lan_if,"-o","chvr0","-j","CHVR_MARK");
    }
    RUN("ip","route","flush","table","188","proto","186");
    RUN("iptables","-t","mangle","-D","PREROUTING","-i","chvr0","-j","CHVR_FLOW");
    RUN("iptables","-t","mangle","-F","CHVR_FLOW");RUN("iptables","-t","mangle","-X","CHVR_FLOW");
    RUN("iptables","-t","mangle","-F","CHVR_MARK");RUN("iptables","-t","mangle","-X","CHVR_MARK");
    if(*policy_lan_if)RUN("iptables","-t","nat","-D","PREROUTING","-i",policy_lan_if,"-p","tcp","--dport","443","-j","CHVR_TLS");
    RUN("iptables","-t","nat","-F","CHVR_TLS");RUN("iptables","-t","nat","-X","CHVR_TLS");
    RUN("iptables","-D","INPUT","-j","CHVR_IN");RUN("iptables","-D","FORWARD","-j","CHVR_FWD");RUN("iptables","-t","nat","-D","POSTROUTING","-j","CHVR_NAT");
    RUN("iptables","-F","CHVR_IN");RUN("iptables","-X","CHVR_IN");RUN("iptables","-F","CHVR_FWD");RUN("iptables","-X","CHVR_FWD");
    RUN("iptables","-t","nat","-F","CHVR_NAT");RUN("iptables","-t","nat","-X","CHVR_NAT");
    carrier_skiplog_port=0;
    tls_active=0;
    tls_prepared=0;
}

int vr_platform_healthy(void)
{
    if(tls_active&&RUN("iptables","-t","nat","-C","PREROUTING","-i",policy_lan_if,"-p","tcp","--dport","443","-j","CHVR_TLS"))return 0;
    if(carrier_skiplog_port) {
        char port[8];snprintf(port,sizeof(port),"%d",carrier_skiplog_port);
        if(RUN("iptables","-C","CHVR_IN","-p","tcp","--dport",port,"-j","SKIPLOG"))return 0;
    }
    return !RUN("iptables","-C","INPUT","-j","CHVR_IN") &&
        !RUN("iptables","-C","FORWARD","-j","CHVR_FWD") &&
        !RUN("iptables","-t","nat","-C","POSTROUTING","-j","CHVR_NAT");
}
