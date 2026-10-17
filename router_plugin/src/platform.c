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
        char buf[512];ssize_t n;
        while((n=read(pipes[0],buf,sizeof(buf)))>0)if(out&&capacity) {
            size_t k=(size_t)n;if(k>capacity-1-used)k=capacity-1-used;memcpy(out+used,buf,k);used+=k;
        }
        if(waitpid(pid,&status,WNOHANG)==pid){done=1;break;}
        struct pollfd p={pipes[0],POLLIN,0};poll(&p,1,20);
    }
    if(!done){kill(pid,SIGKILL);while(waitpid(pid,&status,0)<0&&errno==EINTR){}}
    close(pipes[0]);if(out&&capacity){while(used&&(out[used-1]=='\n'||out[used-1]=='\r'))used--;out[used]=0;}
    return done&&WIFEXITED(status)&&WEXITSTATUS(status)==0?0:-1;
}

#define RUN(...) command((const char *const[]){__VA_ARGS__,NULL},NULL,0)
static int nvram(const char *key,char *out,size_t n){return command((const char *const[]){"nvram","get",key,NULL},out,n);}
static int ipv4(const char *s){struct in_addr a;return inet_pton(AF_INET,s,&a)==1&&a.s_addr&&ntohl(a.s_addr)<0xe0000000u;}
static int iface(const char *s){if(!*s)return 0;for(;*s;s++)if(!((*s>='a'&&*s<='z')||(*s>='A'&&*s<='Z')||(*s>='0'&&*s<='9')||*s=='_'||*s=='-'||*s=='.'))return 0;return 1;}

int vr_addresses_get(vr_addresses *a)
{
    memset(a,0,sizeof(*a));char unit[8],key[48];
    if(nvram("lan_ipaddr",a->lan,sizeof(a->lan)) || nvram("lan_ifname",a->lan_if,sizeof(a->lan_if)))return -1;
    if(!ipv4(a->lan)||!iface(a->lan_if))return -1;
    nvram("wan_primary",unit,sizeof(unit));int primary=!strcmp(unit,"1")?1:0;
    snprintf(key,sizeof(key),"wan%d_ipaddr",primary);nvram(key,a->wan,sizeof(a->wan));if(!ipv4(a->wan))*a->wan=0;
    snprintf(key,sizeof(key),"wan%d_gw_ifname",primary);nvram(key,a->wan_if,sizeof(a->wan_if));
    if(!iface(a->wan_if)){snprintf(key,sizeof(key),"wan%d_ifname",primary);nvram(key,a->wan_if,sizeof(a->wan_if));}
    if(!iface(a->wan_if))*a->wan_if=0;
    return 0;
}

int vr_bind4(const char *ip,int port,int tcp)
{
    struct sockaddr_in sa={0};sa.sin_family=AF_INET;sa.sin_port=htons((uint16_t)port);
    if(!ipv4(ip)||port<1||port>65535||inet_pton(AF_INET,ip,&sa.sin_addr)!=1)return -1;
    int fd=socket(AF_INET,(tcp?SOCK_STREAM:SOCK_DGRAM)|SOCK_CLOEXEC|SOCK_NONBLOCK,0);if(fd<0)return -1;
    int yes=1;setsockopt(fd,SOL_SOCKET,SO_REUSEADDR,&yes,sizeof(yes));
    if(bind(fd,(struct sockaddr *)&sa,sizeof(sa))||(tcp&&listen(fd,8))){close(fd);return -1;}return fd;
}

int vr_platform_start(const vr_config *c,const vr_addresses *a,const char *ip,const char *cidr)
{
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
    if(RUN("iptables","-A","CHVR_IN","-i",a->lan_if,"-d",a->lan,"-p","tcp","--dport",port,"-j","ACCEPT") ||
       RUN("iptables","-A","CHVR_IN","!","-i",a->lan_if,"-d",a->lan,"-p","tcp","--dport",port,"-j","DROP") ||
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
int vr_platform_routes(json_object *policy)
{
    json_object *rules=vr_member(policy,"route_rules");size_t installed=0;
    char seen[128][19];
    for(size_t i=0;i<(size_t)json_object_array_length(rules);i++) {
        json_object *rule=json_object_array_get_idx(rules,i);const char *id=vr_string(rule,"id");
        if((strncmp(id,"gateway-proxy-",14)&&strncmp(id,"linux-router-",13))||strcmp(vr_string(rule,"action"),"probe_exit"))continue;
        json_object *entries=vr_member(rule,"entries");
        for(size_t j=0;j<(size_t)json_object_array_length(entries);j++) {
            const char *entry=json_object_get_string(json_object_array_get_idx(entries,j));char cidr[19];
            if(!entry||strncmp(entry,"cidr:",5)||vr_private_cidr(entry+5,cidr)||connected_cidr(cidr))continue;
            size_t k;for(k=0;k<installed;k++)if(!strcmp(cidr,seen[k]))break;
            if(k<installed)continue;
            if(installed>=128||RUN("ip","route","add",cidr,"dev","chvr0","proto","186"))return -1;
            strcpy(seen[installed++],cidr);
        }
    }
    return 0;
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
    RUN("iptables","-D","INPUT","-j","CHVR_IN");RUN("iptables","-D","FORWARD","-j","CHVR_FWD");RUN("iptables","-t","nat","-D","POSTROUTING","-j","CHVR_NAT");
    RUN("iptables","-F","CHVR_IN");RUN("iptables","-X","CHVR_IN");RUN("iptables","-F","CHVR_FWD");RUN("iptables","-X","CHVR_FWD");
    RUN("iptables","-t","nat","-F","CHVR_NAT");RUN("iptables","-t","nat","-X","CHVR_NAT");
}

int vr_platform_healthy(void)
{
    return !RUN("iptables","-C","INPUT","-j","CHVR_IN") &&
        !RUN("iptables","-C","FORWARD","-j","CHVR_FWD") &&
        !RUN("iptables","-t","nat","-C","POSTROUTING","-j","CHVR_NAT");
}
