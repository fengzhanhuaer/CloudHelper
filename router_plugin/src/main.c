#define _GNU_SOURCE
#include "service.h"
#include "platform.h"
#include "transport.h"
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/file.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>
#include <curl/curl.h>
#include <openssl/crypto.h>

static volatile sig_atomic_t interrupted;
static void on_signal(int signo){(void)signo;interrupted=1;}

int vr_service_stopped(vr_service *s,unsigned generation)
{
    pthread_mutex_lock(&s->mutex);int stopped=s->stop || s->generation!=generation;pthread_mutex_unlock(&s->mutex);return stopped;
}

void vr_service_status(vr_service *s,json_object *o,unsigned generation)
{
    pthread_mutex_lock(&s->mutex);
    if(s->generation==generation){json_object_put(s->status);s->status=o;}else json_object_put(o);
    pthread_mutex_unlock(&s->mutex);
}

static void *worker(void *arg)
{
    vr_service *s=arg;
    while(1) {
        pthread_mutex_lock(&s->mutex);
        while(!s->stop && !s->config.enabled)pthread_cond_wait(&s->changed,&s->mutex);
        if(s->stop){pthread_mutex_unlock(&s->mutex);break;}
        vr_config c=s->config;unsigned generation=s->generation;pthread_mutex_unlock(&s->mutex);
        vr_engine_run(s,&c,generation);
        OPENSSL_cleanse(&c,sizeof(c));
        pthread_mutex_lock(&s->mutex);
        if(!s->stop && s->generation==generation && !(s->pending_policy&&s->pending_generation==generation)) {
            struct timespec until;clock_gettime(CLOCK_REALTIME,&until);until.tv_sec+=15;
            pthread_cond_timedwait(&s->changed,&s->mutex,&until);
        }
        pthread_mutex_unlock(&s->mutex);
    }
    return NULL;
}

static json_object *error_reply(const char *message)
{
    json_object *o=json_object_new_object();json_object_object_add(o,"ok",json_object_new_boolean(0));
    json_object_object_add(o,"error",json_object_new_string(message));return o;
}

static json_object *status_reply(vr_service *s)
{
    json_object *o=json_object_new_object();
    json_object_object_add(o,"version",json_object_new_string(VR_VERSION));
    pthread_mutex_lock(&s->mutex);
    json_object_object_add(o,"ok",json_object_new_boolean(1));json_object_object_add(o,"csrf",json_object_new_string(s->csrf));
    json_object_object_add(o,"config",vr_config_json(&s->config,0));
    json_object_object_add(o,"key_set",json_object_new_boolean(*s->config.node_key!=0));
    const char *encoded=s->status?json_object_to_json_string_ext(s->status,JSON_C_TO_STRING_PLAIN):"{}";
    json_object *status=vr_json_parse(encoded,strlen(encoded));
    json_object_object_add(o,"status",status);
    encoded=s->measurement?json_object_to_json_string_ext(s->measurement,JSON_C_TO_STRING_PLAIN):"{}";
    json_object_object_add(o,"measurement",vr_json_parse(encoded,strlen(encoded)));
    /* Serialization also occurs under the caller's single admin thread. Worker
       replaces objects and never mutates published snapshots. */
    pthread_mutex_unlock(&s->mutex);return o;
}

json_object *vr_service_dispatch(vr_service *s,json_object *request)
{
    const char *action=vr_string(request,"action");
    if(!strcmp(action,"status"))return status_reply(s);
    if(!strcmp(action,"logs"))return vr_service_logs(s);
    const char *token=vr_string(request,"csrf");
    if(strlen(token)!=48 || CRYPTO_memcmp(token,s->csrf,48))return error_reply("invalid CSRF token");
    if(!strcmp(action,"configure")) {
        vr_config c;char err[256];
        pthread_mutex_lock(&s->mutex);
        if(vr_config_parse(&c,vr_member(request,"config"),&s->config,err,sizeof(err))) {pthread_mutex_unlock(&s->mutex);return error_reply(err);}
        if(vr_config_save(&c,s->config_path)){pthread_mutex_unlock(&s->mutex);OPENSSL_cleanse(&c,sizeof(c));return error_reply("cannot persist configuration");}
        s->config=c;s->generation++;
        json_object_put(s->pending_policy);s->pending_policy=NULL;
        json_object *status=json_object_new_object();json_object_object_add(status,"enabled",json_object_new_boolean(c.enabled));
        json_object_object_add(status,"ready",json_object_new_boolean(0));json_object_put(s->status);s->status=status;
        pthread_cond_broadcast(&s->changed);pthread_mutex_unlock(&s->mutex);OPENSSL_cleanse(&c,sizeof(c));
        return status_reply(s);
    }
    if(!strcmp(action,"measure")) {
        const char *node=vr_string(request,"node"),*kind=vr_string(request,"kind");
        if(!*node || strlen(node)>128 || (strcmp(kind,"ping") && strcmp(kind,"upload") && strcmp(kind,"download")))return error_reply("invalid measurement request");
        pthread_mutex_lock(&s->mutex);
        if(!json_object_get_boolean(vr_member(s->status,"ready")) || *s->measure_node || json_object_get_boolean(vr_member(s->measurement,"running"))) {
            pthread_mutex_unlock(&s->mutex);return error_reply("service not ready or measurement busy");
        }
        strcpy(s->measure_node,node);strcpy(s->measure_kind,kind);
        json_object_put(s->measurement);s->measurement=json_object_new_object();
        json_object_object_add(s->measurement,"running",json_object_new_boolean(1));
        json_object_object_add(s->measurement,"kind",json_object_new_string(kind));
        pthread_mutex_unlock(&s->mutex);return status_reply(s);
    }
    return error_reply("unknown action");
}

static void respond(int fd,json_object *o)
{
    const char *body=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);size_t n=strlen(body);
    char header[256];int k=snprintf(header,sizeof(header),"HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nCache-Control: no-store\r\nContent-Length: %zu\r\nConnection: close\r\n\r\n",n);
    for(unsigned part=0;part<2;part++) {
        const char *p=part?body:header;size_t left=part?n:(size_t)k;
        while(left){ssize_t sent=send(fd,p,left,MSG_NOSIGNAL);if(sent<0&&errno==EINTR)continue;if(sent<=0)return;p+=sent;left-=(size_t)sent;}
    }
}

static void admin_client(vr_service *s,int fd)
{
    struct ucred peer;socklen_t len=sizeof(peer);
    if(getsockopt(fd,SOL_SOCKET,SO_PEERCRED,&peer,&len) || peer.uid!=geteuid())return;
    struct timeval timeout={2,0};setsockopt(fd,SOL_SOCKET,SO_RCVTIMEO,&timeout,sizeof(timeout));setsockopt(fd,SOL_SOCKET,SO_SNDTIMEO,&timeout,sizeof(timeout));
    char header[4097];size_t used=0;int complete=0;
    while(used<sizeof(header)-1) {
        ssize_t n=recv(fd,header+used,1,0);if(n<0&&errno==EINTR)continue;if(n!=1)break;used++;
        if(used>=4&&!memcmp(header+used-4,"\r\n\r\n",4)){complete=1;break;}
    }
    header[used]=0;if(!complete)return;
    if(strncmp(header,"POST / HTTP/1.0\r\n",17) && strncmp(header,"POST / HTTP/1.1\r\n",17))return;
    size_t body_len=0;int lengths=0;char *line=strstr(header,"\r\n")+2;
    while(*line && *line!='\r') {
        char *end=strstr(line,"\r\n");if(!end)return;*end=0;
        if(!strncasecmp(line,"Content-Length:",15)) {
            char *v=line+15,*tail;while(*v==' ')v++;
            if(*v<'0'||*v>'9')return;
            unsigned long n=strtoul(v,&tail,10);if(*tail || !n || n>16384 || ++lengths>1)return;body_len=(size_t)n;
        }
        if(!strncasecmp(line,"Transfer-Encoding:",18))return;
        line=end+2;
    }
    if(lengths!=1)return;
    char body[16384];used=0;
    while(used<body_len) {ssize_t n=recv(fd,body+used,body_len-used,0);if(n<0&&errno==EINTR)continue;if(n<=0)return;used+=(size_t)n;}
    json_object *request=vr_json_parse(body,used);OPENSSL_cleanse(body,sizeof(body));
    json_object *reply=request?vr_service_dispatch(s,request):error_reply("invalid JSON");respond(fd,reply);json_object_put(reply);json_object_put(request);
}

int vr_service_run(const char *config_path,const char *socket_path,int cleanup)
{
    vr_service s={0};char error[256],lock_path[128],pid_path[128];int lock=-1,fd=-1,rc=1;pthread_t thread;int started=0,bound=0;
    if(strlen(socket_path)>=sizeof(((struct sockaddr_un *)0)->sun_path) || vr_copy(s.config_path,sizeof(s.config_path),config_path))return 1;
    int invalid_config=vr_config_load(&s.config,config_path,error,sizeof(error));
    pthread_mutex_init(&s.mutex,NULL);pthread_cond_init(&s.changed,NULL);
    snprintf(lock_path,sizeof(lock_path),"%s.lock",socket_path);lock=open(lock_path,O_CREAT|O_RDWR|O_CLOEXEC|O_NOFOLLOW,0600);
    if(lock<0 || flock(lock,LOCK_EX|LOCK_NB))goto done;
    /* After a crash, clear only our DNS marker and firewall chains. Holding the
       instance lock prevents cleanup from interfering with an active service. */
    vr_platform_stop(NULL);
    if(cleanup){rc=0;goto done;}
    if(invalid_config || vr_config_save(&s.config,config_path) || vr_random_hex(s.csrf,24)){fprintf(stderr,"configuration or random initialization failed\n");goto done;}
    struct stat st;if(!lstat(socket_path,&st)){if(!S_ISSOCK(st.st_mode)||st.st_uid!=geteuid())goto done;if(unlink(socket_path))goto done;}
    fd=socket(AF_UNIX,SOCK_STREAM|SOCK_CLOEXEC,0);if(fd<0)goto done;
    struct sockaddr_un sa={0};sa.sun_family=AF_UNIX;strcpy(sa.sun_path,socket_path);
    if(bind(fd,(struct sockaddr *)&sa,sizeof(sa)))goto done;
    bound=1;
    snprintf(pid_path,sizeof(pid_path),"%s.pid",socket_path);
    if(chmod(socket_path,0600) || listen(fd,4))goto done;
    char pid_text[32];int pid_size=snprintf(pid_text,sizeof(pid_text),"%ld\n",(long)getpid());
    if(vr_atomic_write(pid_path,pid_text,(size_t)pid_size))goto done;
    s.status=json_object_new_object();json_object_object_add(s.status,"enabled",json_object_new_boolean(s.config.enabled));
    pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,512*1024);
    int create=pthread_create(&thread,&attr,worker,&s);pthread_attr_destroy(&attr);if(create)goto done;started=1;
    fprintf(stdout,"cloudhelper-router %s (%s) management ready\n",VR_VERSION,VR_KIND);fflush(stdout);
    while(!interrupted) {
        struct pollfd p[1]={{fd,POLLIN,0}};int n=poll(p,1,250);if(n<0&&errno==EINTR)continue;if(n<0)break;
        if(n>0 && (p[0].revents&POLLIN)){int client=accept4(fd,NULL,NULL,SOCK_CLOEXEC);if(client>=0){admin_client(&s,client);close(client);}}
    }
    rc=0;
done:
    pthread_mutex_lock(&s.mutex);s.stop=1;pthread_cond_broadcast(&s.changed);pthread_mutex_unlock(&s.mutex);
    if(started)pthread_join(thread,NULL);
    if(fd>=0)close(fd);
    if(bound){unlink(socket_path);unlink(pid_path);}if(lock>=0)close(lock);
    json_object_put(s.status);json_object_put(s.measurement);json_object_put(s.pending_policy);pthread_cond_destroy(&s.changed);pthread_mutex_destroy(&s.mutex);
    OPENSSL_cleanse(&s.config,sizeof(s.config));return rc;
}

int main(int argc,char **argv)
{
    if(argc==3 && !strcmp(argv[1],"--runtime-mount")) {
        if(vr_runtime_mount(argv[2])){perror("plugin runtime mount");return 1;}
        return 0;
    }
    const char *config="/jffs/cloudhelper-router/config.json",*socket="/var/run/cloudhelper-router/admin.sock";int check=0,cleanup=0;
    for(int i=1;i<argc;i++) {
        if(!strcmp(argv[i],"--version")){printf("cloudhelper-router %s %s C TLS-WebSocket\n",VR_VERSION,VR_KIND);return 0;}
        if(!strcmp(argv[i],"--config")&&i+1<argc)config=argv[++i];
        else if(!strcmp(argv[i],"--socket")&&i+1<argc)socket=argv[++i];
        else if(!strcmp(argv[i],"--check-config"))check=1;
        else if(!strcmp(argv[i],"--cleanup"))cleanup=1;
        else {fprintf(stderr,"usage: cloudhelper-router-plugin [--config PATH] [--socket PATH] [--check-config|--version]\n");return 2;}
    }
    umask(0077);signal(SIGPIPE,SIG_IGN);signal(SIGINT,on_signal);signal(SIGTERM,on_signal);
    if(curl_global_init(CURL_GLOBAL_DEFAULT))return 1;
    int rc;
    if(check){vr_config c;char err[256];rc=vr_config_load(&c,config,err,sizeof(err));puts(rc?err:"configuration valid");OPENSSL_cleanse(&c,sizeof(c));}
    else rc=vr_service_run(config,socket,cleanup);
    curl_global_cleanup();return rc?1:0;
}
