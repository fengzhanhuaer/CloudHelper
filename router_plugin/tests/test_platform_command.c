#define _GNU_SOURCE
#include "../src/platform.c"
#include <assert.h>

static int defer_read;
static int spawn_error;
pid_t __wrap_fork(void){errno=ENOMEM;return -1;}
int __real_posix_spawnp(pid_t *pid,const char *file,const posix_spawn_file_actions_t *actions,const posix_spawnattr_t *attr,char *const args[],char *const env[]);
int __wrap_posix_spawnp(pid_t *pid,const char *file,const posix_spawn_file_actions_t *actions,const posix_spawnattr_t *attr,char *const args[],char *const env[])
{
    short flags=0;assert(!posix_spawnattr_getflags(attr,&flags)&&(flags&POSIX_SPAWN_USEVFORK));
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
    puts("PASS: commands use shared-VM spawn when fork is denied, preserve spawn errno and child exit code");
    puts("PASS: child exit race retains complete bounded NVRAM output and exit status");return 0;
}
