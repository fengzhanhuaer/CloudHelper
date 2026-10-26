#define _GNU_SOURCE
#include "../src/platform.c"
#include <assert.h>

static int defer_read;
static int spawn_error;
static int firewall_mock,firewall_check;
static char firewall_commands[8192];
pid_t __wrap_fork(void){errno=ENOMEM;return -1;}
int __real_posix_spawnp(pid_t *pid,const char *file,const posix_spawn_file_actions_t *actions,const posix_spawnattr_t *attr,char *const args[],char *const env[]);
int __wrap_posix_spawnp(pid_t *pid,const char *file,const posix_spawn_file_actions_t *actions,const posix_spawnattr_t *attr,char *const args[],char *const env[])
{
    short flags=0;assert(!posix_spawnattr_getflags(attr,&flags)&&(flags&POSIX_SPAWN_USEVFORK));
    if(firewall_mock&&!strcmp(file,"ip6tables")) {
        int check=0;size_t used=strlen(firewall_commands);
        for(size_t i=1;args[i];i++) {
            if(!strcmp(args[i],"-C"))check=1;
            int n=snprintf(firewall_commands+used,sizeof(firewall_commands)-used,"%s ",args[i]);assert(n>0&&(size_t)n<sizeof(firewall_commands)-used);used+=(size_t)n;
        }
        strcat(firewall_commands,"\n");
        const char *script=check?(firewall_check==4?"exit 4":firewall_check==1?"exit 1":"exit 0"):"exit 0";
        return __real_posix_spawnp(pid,"sh",actions,attr,(char *const[]){"sh","-c",(char *)script,NULL},env);
    }
    return spawn_error?spawn_error:__real_posix_spawnp(pid,file,actions,attr,args,env);
}
ssize_t __real_read(int fd,void *bytes,size_t n);
pid_t __real_waitpid(pid_t pid,int *status,int options);
ssize_t __wrap_read(int fd,void *bytes,size_t n)
{
    if(defer_read){defer_read=0;errno=EAGAIN;return -1;}
    return __real_read(fd,bytes,n);
}
pid_t __wrap_waitpid(pid_t pid,int *status,int options)
{
    /* Force the child to finish between the initial empty read and reap. */
    return __real_waitpid(pid,status,options&~WNOHANG);
}
uint64_t vr_millis(void)
{struct timespec t;clock_gettime(CLOCK_MONOTONIC,&t);return (uint64_t)t.tv_sec*1000+t.tv_nsec/1000000;}

int main(void)
{
    char out[32];defer_read=1;
    assert(!command((const char *const[]){"sh","-c","printf '192.168.52.1\\n'",NULL},out,sizeof(out)));
    assert(!strcmp(out,"192.168.52.1"));
    defer_read=1;assert(!command((const char *const[]){"sh","-c","printf 'longer-than-capacity'",NULL},out,8));
    assert(!strcmp(out,"longer-"));
    defer_read=1;assert(command((const char *const[]){"sh","-c","printf 'error'; exit 7",NULL},out,sizeof(out))<0);
    assert(!strcmp(out,"error"));
    assert(command_code((const char *const[]){"sh","-c","exit 4",NULL},NULL,0)==4);
    spawn_error=ENOMEM;assert(command_code((const char *const[]){"sh","-c","exit 0",NULL},NULL,0)==-ENOMEM);
    spawn_error=0;firewall_mock=1;firewall_check=1;
    vr_config config={0};config.port=12040;vr_addresses addresses={0};strcpy(addresses.lan_if,"br0");
    assert(!vr_platform_carrier6(&config,&addresses));
    assert(strstr(firewall_commands,"-I CHVR6_IN 1 -p tcp --dport 12040 -j DROP"));
    assert(strstr(firewall_commands,"-I CHVR6_IN 1 -i br0 -p tcp --dport 12040 -j ACCEPT"));
    assert(!strstr(firewall_commands,"FORWARD")&&!strstr(firewall_commands,"nat")&&!strstr(firewall_commands,"-P "));
    *firewall_commands=0;firewall_check=4;assert(vr_platform_carrier6(&config,&addresses)<0);
    assert(!strstr(firewall_commands,"-I ")&&!strstr(firewall_commands,"-D ")&&!strstr(firewall_commands,"-F "));
    *firewall_commands=0;firewall_check=0;assert(!vr_platform_carrier6(&config,&addresses));assert(!strstr(firewall_commands,"-I "));
    *firewall_commands=0;firewall_check=1;config.allow_wan=1;assert(!vr_platform_carrier6(&config,&addresses));
    assert(strstr(firewall_commands,"-I CHVR6_IN 1 -p tcp --dport 12040 -j ACCEPT"));
    vr_platform_carrier6_stop();
    puts("PASS: IPv6 carrier firewall retains LAN/WAN permissions, defers failed checks, and never changes forwarding or default policy");
    puts("PASS: commands use shared-VM spawn when fork is denied, preserve spawn errno and child exit code");
    puts("PASS: child exit race retains complete bounded NVRAM output and exit status");return 0;
}
