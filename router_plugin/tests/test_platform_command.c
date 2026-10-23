#define _GNU_SOURCE
#include "../src/platform.c"
#include <assert.h>

static int defer_read;
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
    puts("PASS: child exit race retains complete bounded NVRAM output and exit status");return 0;
}
