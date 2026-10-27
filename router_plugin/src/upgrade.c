#define _GNU_SOURCE
#include "upgrade.h"
#include "transport.h"
#include <arpa/inet.h>
#include <curl/curl.h>
#include <openssl/crypto.h>
#include <openssl/evp.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <spawn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

extern char **environ;

/* The resident plugin cannot fork its full image under Asuswrt's strict
 * commit limit. Keep libc in charge of the shared-VM child until exec. */
static int upgrade_spawn(pid_t *pid,const char *path,char *const argv[],int output,int errors,int lock)
{
 posix_spawn_file_actions_t actions;posix_spawnattr_t attr;
 int code=posix_spawn_file_actions_init(&actions);if(code)return code;
 code=posix_spawnattr_init(&attr);if(code){posix_spawn_file_actions_destroy(&actions);return code;}
 code=posix_spawnattr_setflags(&attr,POSIX_SPAWN_USEVFORK);
 if(!code)code=posix_spawn_file_actions_addopen(&actions,STDIN_FILENO,"/dev/null",O_RDONLY,0);
 if(!code&&output>=0)code=posix_spawn_file_actions_adddup2(&actions,output,STDOUT_FILENO);
 if(!code&&errors>=0)code=posix_spawn_file_actions_adddup2(&actions,errors,STDERR_FILENO);
 if(!code&&lock>=0)code=posix_spawn_file_actions_adddup2(&actions,lock,3);
 long maxfd=sysconf(_SC_OPEN_MAX);if(maxfd<0||maxfd>65536)maxfd=65536;
 for(int fd=3;!code&&fd<maxfd;fd++) {
  if(fd==3&&lock>=0)continue;
  int flags=fcntl(fd,F_GETFD);
  if(flags>=0&&!(flags&FD_CLOEXEC))code=posix_spawn_file_actions_addclose(&actions,fd);
 }
 if(!code)code=posix_spawn(pid,path,&actions,&attr,argv,environ);
 posix_spawnattr_destroy(&attr);posix_spawn_file_actions_destroy(&actions);return code;
}

#define UPGRADE_STATE "/var/run/cloudhelper-router/upgrade.json"
#define UPGRADE_REPO "fengzhanhuaer/CloudHelper"
static const char *members[]={"checksums","device-profile","install.sh","manifest.json","release-id",
 "payload/cloudhelper-router-plugin","payload/service","payload/httpd-bridge.so","payload/web/VirtualRouter.asp",
 ".valid","webs/Module_cloudhelper.asp","scripts/uninstall_cloudhelper.sh"};
typedef struct {
 vr_service *service;vr_config config;unsigned generation;int check_only,direct,lock;
 char directory[80],version[64],iface[16];
} upgrade_job;

const char *vr_upgrade_profile(void)
{
 char b[32];size_t len;
 if(vr_read_file("/jffs/cloudhelper-router/device-profile",b,sizeof(b)-1,&len))return "";
 b[len]=0;if(len&&b[len-1]=='\n')b[len-1]=0;
 return !strcmp(b,"ax56uv2")?"ax56uv2":!strcmp(b,"be86u")?"be86u":"";
}
static int version(const char *s,unsigned v[3])
{
 if(*s=='v')s++;
 for(int i=0;i<3;i++) {char *end;if(*s<'0'||*s>'9')return -1;
  unsigned long x=strtoul(s,&end,10);if(x>1000000||end-s>7)return -1;v[i]=(unsigned)x;s=end;
  if(i<2){if(*s++!='.')return -1;}
 }
 return *s&&*s!='-'&&*s!='+'?-1:0;
}
int vr_upgrade_version_newer(const char *latest,const char *current)
{
 unsigned a[3],b[3];if(version(latest,a)||version(current,b))return 0;
 /* Releases must be exact stable tags. */
 const char *s=latest;if(*s=='v')s++;if(strspn(s,"0123456789.")!=strlen(s))return 0;
 for(int i=0;i<3;i++){if(a[i]!=b[i])return a[i]>b[i];}return 0;
}
static int hex_digest(const char *s)
{
 if(strncmp(s,"sha256:",7)||strlen(s)!=71)return 0;
 for(size_t i=7;i<71;i++)if(!((s[i]>='0'&&s[i]<='9')||(s[i]>='a'&&s[i]<='f')))return 0;
 return 1;
}
json_object *vr_upgrade_select(json_object *release,const char *profile,const char *current,char *err,size_t n)
{
 const char *tag=vr_string(release,"tag_name");
 if(strcmp(profile,"ax56uv2")&&strcmp(profile,"be86u")){snprintf(err,n,"unsupported router profile");return NULL;}
 unsigned parsed[3];if(version(tag,parsed)||strspn(tag+(*tag=='v'),"0123456789.")!=strlen(tag+(*tag=='v'))){snprintf(err,n,"invalid stable release version");return NULL;}
 char name[96],prefix[256];snprintf(name,sizeof(name),"cloudhelper-asus%s-jffs.tar.gz",!strcmp(profile,"be86u")?"be86u":"56uv2");
 snprintf(prefix,sizeof(prefix),"https://github.com/" UPGRADE_REPO "/releases/download/%s/",tag);
 json_object *assets=vr_member(release,"assets"),*selected=NULL;
 for(size_t i=0;i<(size_t)json_object_array_length(assets);i++){
  json_object *a=json_object_array_get_idx(assets,i);if(strcmp(vr_string(a,"name"),name))continue;
  const char *url=vr_string(a,"download_url");if(!*url)url=vr_string(a,"browser_download_url");
  int64_t size=json_object_get_int64(vr_member(a,"size"));
  if(selected||strncmp(url,prefix,strlen(prefix))||strcmp(url+strlen(prefix),name)||!hex_digest(vr_string(a,"digest"))||size<1024||size>VR_UPGRADE_PACKAGE_MAX){json_object_put(selected);snprintf(err,n,"release package metadata or SHA256 is invalid");return NULL;}
  selected=json_object_new_object();json_object_object_add(selected,"url",json_object_new_string(url));
  json_object_object_add(selected,"sha256",json_object_new_string(vr_string(a,"digest")+7));
  json_object_object_add(selected,"bytes",json_object_new_int64(size));
 }
 if(!selected){snprintf(err,n,"this release has no verified package for this router");return NULL;}
 json_object_object_add(selected,"version",json_object_new_string(tag));
 json_object_object_add(selected,"device_profile",json_object_new_string(profile));
 json_object_object_add(selected,"upgrade_available",json_object_new_boolean(vr_upgrade_version_newer(tag,current)));
 return selected;
}
static int read_exact(int fd,void *out,size_t n)
{
 unsigned char *p=out;while(n){ssize_t k=read(fd,p,n);if(k<0&&errno==EINTR)continue;if(k<=0)return -1;p+=k;n-=(size_t)k;}return 0;
}
static int file_sha(const char *path,const char *expected)
{
 int fd=open(path,O_RDONLY|O_NOFOLLOW|O_CLOEXEC);if(fd<0)return -1;
 EVP_MD_CTX *ctx=EVP_MD_CTX_new();unsigned char b[8192],digest[32];unsigned len=0;int bad=!ctx||!EVP_DigestInit_ex(ctx,EVP_sha256(),NULL);ssize_t k;
 while(!bad&&(k=read(fd,b,sizeof(b)))!=0){if(k<0){if(errno==EINTR)continue;bad=1;break;}bad=!EVP_DigestUpdate(ctx,b,(size_t)k);}
 if(!bad)bad=!EVP_DigestFinal_ex(ctx,digest,&len)||len!=32;
 char hex[65];for(unsigned i=0;i<len&&i<32;i++)snprintf(hex+2*i,3,"%02x",digest[i]);
 EVP_MD_CTX_free(ctx);close(fd);return bad||strlen(expected)!=64||CRYPTO_memcmp(hex,expected,64)?-1:0;
}
/* Extract a deliberately small regular-file-only tar format. Never pass release
 * paths to tar: links, prefixes, duplicates, extended headers and traversal fail. */
int vr_upgrade_unpack(const char *archive,const char *directory,const char *profile,char *err,size_t n)
{
 int pipefd[2],bad=0,status=0;unsigned seen=0;size_t total=0;
 if(pipe2(pipefd,O_CLOEXEC)){snprintf(err,n,"package decompressor pipe failed (%d)",errno);return -1;}
 pid_t pid;char *args[]={"gzip","-dc",(char *)archive,NULL};
 int code=upgrade_spawn(&pid,"/bin/gzip",args,pipefd[1],-1,-1);
 close(pipefd[1]);if(code){close(pipefd[0]);snprintf(err,n,"package decompressor start failed (%d)",code);return -1;}
 char path[512];const char *dirs[]={"cloudhelper","cloudhelper/payload","cloudhelper/payload/web","cloudhelper/webs","cloudhelper/scripts"};
 for(size_t i=0;i<5;i++){snprintf(path,sizeof(path),"%s/%s",directory,dirs[i]);if(mkdir(path,0700)){bad=1;break;}}
 while(!bad){unsigned char h[512];if(read_exact(pipefd[0],h,512)){bad=1;break;}
  if(!h[0]){unsigned char tail[512];if(read_exact(pipefd[0],tail,512)){bad=1;break;}for(size_t i=0;i<512;i++)if(h[i]||tail[i])bad=1;break;}
  unsigned sum=0;for(size_t i=0;i<512;i++)sum+=(i>=148&&i<156)?32:h[i];
  char field[13],*end;memcpy(field,h+148,8);field[8]=0;unsigned long expected=strtoul(field,&end,8);
  if(sum!=expected||(*end&&*end!=' ')||h[345]||h[157]||(h[156]&&h[156]!='0')||!memchr(h,0,100)||strncmp((char *)h,"cloudhelper/",12)){bad=1;break;}
  size_t index=12;for(size_t i=0;i<12;i++)if(!strcmp((char *)h+12,members[i]))index=i;
  if(index==12||(seen&(1u<<index))){bad=1;break;}seen|=1u<<index;
  memcpy(field,h+124,12);field[12]=0;unsigned long length=strtoul(field,&end,8);
  if((*end&&*end!=' ')||length>VR_UPGRADE_PACKAGE_MAX||total+length>4u*1024u*1024u){bad=1;break;}total+=length;
  snprintf(path,sizeof(path),"%s/cloudhelper/%s",directory,members[index]);
  int out=open(path,O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC,0600);if(out<0){bad=1;break;}
  size_t left=(size_t)length;while(left&&!bad){unsigned char block[512];size_t take=left>512?512:left;
   if(read_exact(pipefd[0],block,512)||write(out,block,take)!=(ssize_t)take)bad=1;
   left-=take;
  }close(out);
 }
 /* Consume only bounded zero padding so gzip validates its footer/CRC. */
 size_t padding=0;unsigned char b[512];ssize_t k;
 while(!bad&&(k=read(pipefd[0],b,sizeof(b)))>0){padding+=(size_t)k;if(padding>16384)bad=1;for(ssize_t i=0;i<k;i++)if(b[i])bad=1;}
 if(bad)kill(pid,SIGTERM);
 close(pipefd[0]);while(waitpid(pid,&status,0)<0&&errno==EINTR){}
 if(!WIFEXITED(status)||WEXITSTATUS(status)||seen!=4095)bad=1;
 snprintf(path,sizeof(path),"%s/cloudhelper/device-profile",directory);char p[32];size_t used;
 if(!bad&&(vr_read_file(path,p,sizeof(p)-1,&used)||used!=strlen(profile)+1||memcmp(p,profile,strlen(profile))||p[used-1]!='\n'))bad=1;
 snprintf(path,sizeof(path),"%s/cloudhelper/payload/cloudhelper-router-plugin",directory);if(!bad&&chmod(path,0700))bad=1;
 snprintf(path,sizeof(path),"%s/cloudhelper/payload/service",directory);if(!bad&&chmod(path,0700))bad=1;
 if(bad)snprintf(err,n,"invalid, incomplete or incompatible router package");
 return bad?-1:0;
}
static void state(upgrade_job *job,const char *phase,const char *message,int running,int available,int64_t received,int64_t bytes)
{
 json_object *o=json_object_new_object();json_object_object_add(o,"phase",json_object_new_string(phase));
 json_object_object_add(o,"message",json_object_new_string(message));json_object_object_add(o,"running",json_object_new_boolean(running));
 json_object_object_add(o,"latest_version",json_object_new_string(job->version));json_object_object_add(o,"upgrade_available",json_object_new_boolean(available));
 json_object_object_add(o,"downloaded_bytes",json_object_new_int64(received));json_object_object_add(o,"total_bytes",json_object_new_int64(bytes));
 if(job->service){pthread_mutex_lock(&job->service->mutex);json_object_put(job->service->upgrade);job->service->upgrade=json_object_get(o);pthread_mutex_unlock(&job->service->mutex);}
 const char *text=json_object_to_json_string_ext(o,JSON_C_TO_STRING_PLAIN);vr_atomic_write(UPGRADE_STATE,text,strlen(text));json_object_put(o);
}
json_object *vr_upgrade_status(vr_service *s)
{
 pthread_mutex_lock(&s->mutex);json_object *o=s->upgrade?json_object_get(s->upgrade):NULL;pthread_mutex_unlock(&s->mutex);
 if(!o){char b[4096];size_t n;if(!vr_read_file(UPGRADE_STATE,b,sizeof(b),&n))o=vr_json_parse(b,n);}
 return o?o:json_object_new_object();
}
typedef struct {upgrade_job *job;int fd;int64_t offset,size,received,end;long status;int range_valid,preparing;char *memory;size_t used;} transfer;
static int cancelled(void *arg,curl_off_t a,curl_off_t b,curl_off_t c,curl_off_t d)
{
 (void)a;(void)b;(void)c;(void)d;transfer *t=arg;return t->job->service&&vr_service_stopped(t->job->service,t->job->generation);
}
static size_t header(void *p,size_t size,size_t count,void *arg)
{
 transfer *t=arg;size_t n=size*count;char b[512];if(n>=sizeof(b))return n;memcpy(b,p,n);b[n]=0;
 if(!strncasecmp(b,"X-CloudHelper-Cache: preparing",29))t->preparing=1;
 if(!strncmp(b,"HTTP/",5)){char *space=strchr(b,' ');t->status=space?strtol(space+1,NULL,10):0;t->range_valid=0;if(t->fd<0)t->used=0;}
 if(!strncasecmp(b,"Content-Range:",14)){long long first,last,total;if(sscanf(b+14," bytes %lld-%lld/%lld",&first,&last,&total)==3&&first==t->offset&&last>=first&&last<t->size&&total==t->size&&(!t->end||last==t->end))t->range_valid=1;}
 return n;
}
static size_t receive(void *p,size_t size,size_t count,void *arg)
{
 transfer *t=arg;if(size&&count>VR_UPGRADE_PACKAGE_MAX/size)return 0;size_t n=size*count;
 if(t->job->direct&&t->status>=300&&t->status<400)return n;
 if(t->fd<0){if(t->used+n>512u*1024u)return 0;char *b=realloc(t->memory,t->used+n+1);if(!b)return 0;t->memory=b;memcpy(b+t->used,p,n);t->used+=n;b[t->used]=0;return n;}
 if(t->status!=200&&t->status!=206)return 0;
 if(t->end&&t->status!=206)return 0;
 if(t->status==206&&!t->range_valid)return 0;
 if(t->offset&&t->status==200){if(ftruncate(t->fd,0)||lseek(t->fd,0,SEEK_SET)<0)return 0;t->offset=0;t->received=0;}
 if(t->offset+t->received+(int64_t)n>t->size)return 0;
 if(t->end&&t->offset+t->received+(int64_t)n>t->end+1)return 0;
 if(write(t->fd,p,n)!=(ssize_t)n)return 0;
 t->received+=(int64_t)n;return n;
}
static CURLcode fetch(upgrade_job *job,const char *remote,transfer *t)
{
 char url[2048],path[128]="/api/probe/proxy/download",challenge[513],signature[65],line[2200],err[256],selected[1024],pin[65]="";int origin_direct=0;
 struct curl_slist *headers=NULL,*addresses=NULL;CURL *curl=curl_easy_init();if(!curl)return CURLE_FAILED_INIT;
 if(job->direct)snprintf(url,sizeof(url),"%s",remote);
 else {
  if(vr_controller_auth(&job->config,job->iface,"GET",path,challenge,signature,err,sizeof(err))){curl_easy_cleanup(curl);return CURLE_LOGIN_DENIED;}
  origin_direct=vr_controller_link_target(&job->config,selected,pin);
  snprintf(url,sizeof(url),"%s%s",selected,path);
  snprintf(line,sizeof(line),"X-Probe-Node-Id: %s",job->config.node_id);headers=curl_slist_append(headers,line);
  snprintf(line,sizeof(line),"X-Probe-Rand: %s",challenge);headers=curl_slist_append(headers,line);
  snprintf(line,sizeof(line),"X-Probe-Signature: %s",signature);headers=curl_slist_append(headers,line);
  snprintf(line,sizeof(line),"X-CloudHelper-Download-URL: %s",remote);headers=curl_slist_append(headers,line);
  headers=curl_slist_append(headers,"X-CloudHelper-Download-Cache: 1");
 }
 char origin[1024],host[254],entry[384];int port,tls;
 /* Resolve the initial TLS origin through the same independent DNS path as
    control traffic. Redirects are HTTPS-only and never carry proxy credentials. */
 const char *slash=strchr(url+8,'/');size_t length=slash?(size_t)(slash-url):strlen(url);
 if(length>=sizeof(origin)){curl_easy_cleanup(curl);curl_slist_free_all(headers);return CURLE_URL_MALFORMAT;}
 memcpy(origin,url,length);origin[length]=0;
 if(vr_origin(origin,host,&port,&tls)||!tls){curl_easy_cleanup(curl);curl_slist_free_all(headers);return CURLE_URL_MALFORMAT;}
 /* Select a TCP/TLS-ready address without sending any download credentials.
    The actual request remains single-shot and is authenticated normally. */
 int connected=-1;SSL *probe=vr_tls_connect(host,port,pin,&job->config,job->iface,&connected,vr_millis()+8000);
 if(!probe){curl_easy_cleanup(curl);curl_slist_free_all(headers);return CURLE_COULDNT_CONNECT;}
 struct sockaddr_storage peer;socklen_t plen=sizeof(peer);char selected_ip[46]="";
 if(!getpeername(connected,(struct sockaddr *)&peer,&plen)){
  if(peer.ss_family==AF_INET)inet_ntop(AF_INET,&((struct sockaddr_in *)&peer)->sin_addr,selected_ip,sizeof(selected_ip));
  else if(peer.ss_family==AF_INET6)inet_ntop(AF_INET6,&((struct sockaddr_in6 *)&peer)->sin6_addr,selected_ip,sizeof(selected_ip));
 }
 SSL_free(probe);close(connected);
 if(!*selected_ip||vr_curl_address(host,port,selected_ip,entry,sizeof(entry))){curl_easy_cleanup(curl);curl_slist_free_all(headers);return CURLE_COULDNT_CONNECT;}
 if(*entry)addresses=curl_slist_append(NULL,entry);
 curl_easy_setopt(curl,CURLOPT_URL,url);curl_easy_setopt(curl,CURLOPT_RESOLVE,addresses);curl_easy_setopt(curl,CURLOPT_PROXY,"");
 curl_easy_setopt(curl,CURLOPT_PROTOCOLS,(long)CURLPROTO_HTTPS);curl_easy_setopt(curl,CURLOPT_REDIR_PROTOCOLS,(long)CURLPROTO_HTTPS);
 curl_easy_setopt(curl,CURLOPT_FOLLOWLOCATION,job->direct?1L:0L);curl_easy_setopt(curl,CURLOPT_MAXREDIRS,4L);
 curl_easy_setopt(curl,CURLOPT_SSL_VERIFYPEER,1L);curl_easy_setopt(curl,CURLOPT_SSL_VERIFYHOST,2L);curl_easy_setopt(curl,CURLOPT_SSLVERSION,(long)CURL_SSLVERSION_TLSv1_2);
 curl_easy_setopt(curl,CURLOPT_USERAGENT,"CloudHelper-router/" VR_VERSION);curl_easy_setopt(curl,CURLOPT_HTTPHEADER,headers);
 curl_easy_setopt(curl,CURLOPT_ACCEPT_ENCODING,"identity");curl_easy_setopt(curl,CURLOPT_CONNECTTIMEOUT,8L);curl_easy_setopt(curl,CURLOPT_TIMEOUT,90L);
 curl_easy_setopt(curl,CURLOPT_LOW_SPEED_LIMIT,1L);curl_easy_setopt(curl,CURLOPT_LOW_SPEED_TIME,20L);curl_easy_setopt(curl,CURLOPT_NOSIGNAL,1L);
 if(*job->iface&&!strchr(selected_ip,':'))curl_easy_setopt(curl,CURLOPT_INTERFACE,job->iface);
 curl_easy_setopt(curl,CURLOPT_NOPROGRESS,0L);curl_easy_setopt(curl,CURLOPT_XFERINFOFUNCTION,cancelled);curl_easy_setopt(curl,CURLOPT_XFERINFODATA,t);
 curl_easy_setopt(curl,CURLOPT_HEADERFUNCTION,header);curl_easy_setopt(curl,CURLOPT_HEADERDATA,t);
 curl_easy_setopt(curl,CURLOPT_WRITEFUNCTION,receive);curl_easy_setopt(curl,CURLOPT_WRITEDATA,t);
 char range[64];if(t->end){snprintf(range,sizeof(range),"%lld-%lld",(long long)t->offset,(long long)t->end);curl_easy_setopt(curl,CURLOPT_RANGE,range);}
 else if(t->offset){snprintf(range,sizeof(range),"%lld-",(long long)t->offset);curl_easy_setopt(curl,CURLOPT_RANGE,range);}
 vr_curl_pin(curl,pin);
 uint64_t started=vr_millis();CURLcode rc=curl_easy_perform(curl);curl_easy_getinfo(curl,CURLINFO_RESPONSE_CODE,&t->status);
 double headers_at=0;curl_easy_getinfo(curl,CURLINFO_STARTTRANSFER_TIME,&headers_at);
 if(!job->direct&&!t->preparing&&rc!=CURLE_ABORTED_BY_CALLBACK)vr_controller_link_feedback(&job->config,origin_direct,rc==CURLE_OK&&t->status<500,headers_at>0?(uint64_t)(headers_at*1000):vr_millis()-started);
 curl_easy_cleanup(curl);curl_slist_free_all(headers);curl_slist_free_all(addresses);return rc;
}

typedef struct {upgrade_job *job;const char *url;FILE *file;int64_t first,last,total;int ok;} parallel_chunk;
static void *fetch_chunk(void *arg)
{
 parallel_chunk *c=arg;
 for(int attempt=0;attempt<3;attempt++) {
  off_t size=lseek(fileno(c->file),0,SEEK_END);if(size<0)return NULL;
  if(size==c->last-c->first+1){c->ok=1;return NULL;}
  transfer t={.job=c->job,.fd=fileno(c->file),.offset=c->first+size,.size=c->total,.end=c->last};
  CURLcode rc=fetch(c->job,c->url,&t);
  if(rc==CURLE_OK&&t.status==206&&lseek(t.fd,0,SEEK_END)==c->last-c->first+1){c->ok=1;return NULL;}
  if(t.preparing||cancelled(&t,0,0,0,0))return NULL;
 }
 return NULL;
}
static int fetch_parallel(upgrade_job *job,const char *url,int fd,int64_t bytes)
{
 if(bytes<256*1024)return -1;
 parallel_chunk chunks[2]={{.job=job,.url=url,.first=0,.last=bytes/2-1,.total=bytes},{.job=job,.url=url,.first=bytes/2,.last=bytes-1,.total=bytes}};
 pthread_t threads[2];int started=0,ok=0;pthread_attr_t attr;pthread_attr_init(&attr);pthread_attr_setstacksize(&attr,256*1024);
 for(int i=0;i<2;i++) {chunks[i].file=tmpfile();if(!chunks[i].file)goto done;}
 for(int i=0;i<2;i++) {if(pthread_create(&threads[i],&attr,fetch_chunk,&chunks[i]))break;started++;}
 for(int i=0;i<started;i++)pthread_join(threads[i],NULL);
 if(started!=2||!chunks[0].ok||!chunks[1].ok)goto done;
 for(int i=0;i<2;i++) {
  if(lseek(fileno(chunks[i].file),0,SEEK_SET)<0)goto done;
  char buffer[16384];ssize_t n;
  while((n=read(fileno(chunks[i].file),buffer,sizeof(buffer)))>0)if(write(fd,buffer,(size_t)n)!=n)goto done;
  if(n<0)goto done;
 }
 ok=1;
done:
 pthread_attr_destroy(&attr);
 for(int i=0;i<2;i++)if(chunks[i].file)fclose(chunks[i].file);
 if(!ok){if(ftruncate(fd,0)||lseek(fd,0,SEEK_SET)<0)return -1;}return ok?0:-1;
}
static void cleanup(const char *dir)
{
 char p[512];for(size_t i=0;i<12;i++){snprintf(p,sizeof(p),"%s/cloudhelper/%s",dir,members[i]);unlink(p);}
 const char *paths[]={"cloudhelper/payload/web","cloudhelper/payload","cloudhelper/webs","cloudhelper/scripts","cloudhelper"};
 for(size_t i=0;i<5;i++){snprintf(p,sizeof(p),"%s/%s",dir,paths[i]);rmdir(p);}
 snprintf(p,sizeof(p),"%s/package.tar.gz",dir);unlink(p);snprintf(p,sizeof(p),"%s/job.json",dir);unlink(p);rmdir(dir);
}
static void *run(void *arg)
{
 upgrade_job *job=arg;char err[240]="upgrade failed",path[512];json_object *release=NULL,*asset=NULL;
 state(job,"checking","正在检查发布版本",1,0,0,0);
 if(job->direct){transfer t={.job=job,.fd=-1};CURLcode rc=fetch(job,"https://api.github.com/repos/" UPGRADE_REPO "/releases/latest",&t);if(rc==CURLE_OK&&t.status==200)release=vr_json_parse(t.memory,t.used);free(t.memory);}
 else release=vr_controller_call(&job->config,job->iface,"GET","/api/probe/proxy/github/latest?repo=" UPGRADE_REPO,NULL,err,sizeof(err));
 if(!release){snprintf(err,sizeof(err),"无法获取发布信息，请稍后重试");goto failed;}
 asset=vr_upgrade_select(release,vr_upgrade_profile(),VR_VERSION,err,sizeof(err));if(!asset)goto failed;
 vr_copy(job->version,sizeof(job->version),vr_string(asset,"version"));
 int available=json_object_get_boolean(vr_member(asset,"upgrade_available"));
 if(job->check_only||!available){state(job,"checked",available?"发现新版本":"当前已是最新版本",0,available,0,0);goto done;}
 snprintf(job->directory,sizeof(job->directory),"/tmp/chvr-upgrade.XXXXXX");if(!mkdtemp(job->directory)){snprintf(err,sizeof(err),"cannot create private upgrade workspace");goto failed;}
 snprintf(path,sizeof(path),"%s/package.tar.gz",job->directory);int fd=open(path,O_RDWR|O_CREAT|O_EXCL|O_CLOEXEC|O_NOFOLLOW,0600);
 if(fd<0)goto failed;
 int64_t bytes=json_object_get_int64(vr_member(asset,"bytes"));int complete=0;
 if(!fetch_parallel(job,vr_string(asset,"url"),fd,bytes))complete=1;
 for(int attempt=0,preparing=0;!complete&&attempt<6;attempt++) {
  off_t offset=lseek(fd,0,SEEK_END);if(offset<0)break;
  state(job,"downloading",attempt?"正在续传升级包":"正在下载升级包",1,1,offset,bytes);
  transfer t={.job=job,.fd=fd,.offset=offset,.size=bytes};CURLcode rc=fetch(job,vr_string(asset,"url"),&t);
  off_t got=lseek(fd,0,SEEK_END);if(rc==CURLE_OK&&(t.status==200||t.status==206)&&got==bytes){complete=1;break;}
  if(cancelled(&t,0,0,0,0))break;
  if(t.status==503&&t.preparing&&preparing++<900){attempt--;for(int i=0;i<20&&!cancelled(&t,0,0,0,0);i++)poll(NULL,0,100);continue;}
  for(int i=0;i<(1<<attempt)*10;i++){if(cancelled(&t,0,0,0,0))break;poll(NULL,0,100);}
 }
 close(fd);if(!complete){snprintf(err,sizeof(err),"下载未完成，当前版本保持运行；请重试");goto failed;}
 state(job,"verifying","正在校验升级包",1,1,bytes,bytes);
 if(file_sha(path,vr_string(asset,"sha256"))){snprintf(err,sizeof(err),"升级包 SHA256 校验失败，当前版本保持运行");goto failed;}
 if(vr_upgrade_unpack(path,job->directory,vr_upgrade_profile(),err,sizeof(err)))goto failed;
 char manifest[8192];size_t used;snprintf(path,sizeof(path),"%s/cloudhelper/manifest.json",job->directory);
 if(vr_read_file(path,manifest,sizeof(manifest),&used)){snprintf(err,sizeof(err),"invalid package manifest");goto failed;}
 json_object *m=vr_json_parse(manifest,used);int valid=m&&!strcmp(vr_string(m,"version"),job->version)&&!strcmp(vr_string(m,"device_profile"),vr_upgrade_profile());json_object_put(m);if(!valid){snprintf(err,sizeof(err),"package version or router profile mismatch");goto failed;}
 snprintf(path,sizeof(path),"%s/job.json",job->directory);const char *text=json_object_to_json_string_ext(asset,JSON_C_TO_STRING_PLAIN);
 if(vr_atomic_write(path,text,strlen(text)))goto failed;
 state(job,"applying","正在安装，管理页面将短暂重连",1,1,bytes,bytes);
 /* The installed service lives on a removable tmpfs. Run the helper from the
  * verified new payload so stopping the old service can unmount that tmpfs. */
 snprintf(path,sizeof(path),"%s/cloudhelper/payload/cloudhelper-router-plugin",job->directory);
 int console=open("/dev/console",O_WRONLY|O_CLOEXEC);
 pid_t pid;char *argv[]={"cloudhelper-router-plugin","--apply-upgrade",job->directory,NULL};
 int code=upgrade_spawn(&pid,path,argv,console,console,job->lock);
 if(console>=0)close(console);
 if(code){snprintf(err,sizeof(err),"upgrade installer start failed (%d)",code);goto failed;}
 goto done;
failed:
 state(job,"failed",err,0,0,0,0);if(job->service)vr_service_log(job->service,"upgrade",err);
 if(*job->directory)cleanup(job->directory);
done:
 json_object_put(asset);json_object_put(release);close(job->lock);OPENSSL_cleanse(&job->config,sizeof(job->config));free(job);return NULL;
}
int vr_upgrade_start(vr_service *s,json_object *request,char *err,size_t n)
{
 const char *mode=vr_string(request,"mode"),*repo=vr_string(request,"release_repo");
 if((*mode&&strcmp(mode,"direct")&&strcmp(mode,"proxy"))||(*repo&&strcmp(repo,UPGRADE_REPO))){snprintf(err,n,"unsupported upgrade source");return -1;}
 if(!*vr_upgrade_profile()){snprintf(err,n,"router profile unavailable; reinstall the matching plugin package");return -1;}
 int lock=open("/var/run/cloudhelper-router/upgrade.lock",O_RDWR|O_CREAT|O_CLOEXEC|O_NOFOLLOW,0600);
 if(lock<0||flock(lock,LOCK_EX|LOCK_NB)){if(lock>=0)close(lock);snprintf(err,n,"upgrade already running");return -1;}
 vr_upgrade_join(s);upgrade_job *job=calloc(1,sizeof(*job));if(!job){close(lock);return -1;}
 job->lock=lock;job->service=s;job->direct=!strcmp(mode,"direct");job->check_only=json_object_get_boolean(vr_member(request,"check_only"));
 pthread_mutex_lock(&s->mutex);job->config=s->config;job->generation=s->generation;pthread_mutex_unlock(&s->mutex);
 if(!job->direct&&(!*job->config.controller||!*job->config.node_id||!*job->config.node_key)){snprintf(err,n,"请先保存主控和节点身份");close(lock);OPENSSL_cleanse(job,sizeof(*job));free(job);return -1;}
 /* The independent resolver prevents a dependency on the plugin DNS listener. */
 if(pthread_create(&s->upgrade_thread,NULL,run,job)){close(lock);OPENSSL_cleanse(job,sizeof(*job));free(job);snprintf(err,n,"cannot start upgrade worker");return -1;}
 s->upgrade_joinable=1;return 0;
}
void vr_upgrade_join(vr_service *s){if(s->upgrade_joinable){pthread_join(s->upgrade_thread,NULL);s->upgrade_joinable=0;}}
int vr_upgrade_apply(const char *dir)
{
 (void)setsid();
 struct stat st;if(geteuid()||strncmp(dir,"/tmp/chvr-upgrade.",18)||strlen(dir)!=24||lstat(dir,&st)||!S_ISDIR(st.st_mode)||st.st_uid||st.st_mode&077)return 1;
 char p[512],b[4096];size_t used;snprintf(p,sizeof(p),"%s/job.json",dir);if(vr_read_file(p,b,sizeof(b),&used))return 1;
 json_object *o=vr_json_parse(b,used);upgrade_job job={0};vr_copy(job.version,sizeof(job.version),vr_string(o,"version"));
 snprintf(p,sizeof(p),"%s/package.tar.gz",dir);if(!o||file_sha(p,vr_string(o,"sha256"))){json_object_put(o);return 1;}
 char previous[32]={0};size_t previous_size=0;
 if(!vr_read_file("/jffs/cloudhelper-router/active",previous,sizeof(previous)-1,&previous_size))previous[previous_size]=0;
 snprintf(p,sizeof(p),"%s/cloudhelper/install.sh",dir);pid_t pid;int status=0;
 char *install[]={"sh",p,NULL};int code=upgrade_spawn(&pid,"/bin/sh",install,-1,-1,-1);
 if(code)status=-1;else while(waitpid(pid,&status,0)<0&&errno==EINTR){}
 int ok=status>=0&&WIFEXITED(status)&&!WEXITSTATUS(status);
 if(!ok&&*previous){
  char current[32]={0};size_t current_size=0;
  if(!vr_read_file("/jffs/cloudhelper-router/active",current,sizeof(current)-1,&current_size)&&
     (current_size!=previous_size||memcmp(current,previous,previous_size))){
   char *rollback[]={"sh",p,"--rollback",NULL};code=upgrade_spawn(&pid,"/bin/sh",rollback,-1,-1,-1);
   if(!code)while(waitpid(pid,&status,0)<0&&errno==EINTR){}
  }
  /* A failed stop can leave the unchanged old release stopped. Restore its
   * service even when no active-release swap occurred. */
  char *start[]={"sh","/jffs/cloudhelper-router/service","start",NULL};code=upgrade_spawn(&pid,"/bin/sh",start,-1,-1,-1);
  if(!code)while(waitpid(pid,&status,0)<0&&errno==EINTR){}
 }
 state(&job,ok?"complete":"failed",ok?"升级完成，请刷新页面":"安装失败，已尝试恢复上一版本；请检查当前状态",0,0,0,0);
 json_object_put(o);cleanup(dir);return ok?0:1;
}
