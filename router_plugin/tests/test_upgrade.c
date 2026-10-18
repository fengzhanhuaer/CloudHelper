#define _GNU_SOURCE
#include "../src/upgrade.c"
#include <assert.h>

int vr_service_stopped(vr_service *s,unsigned generation)
{return s->stop||s->generation!=generation;}

int main(int argc,char **argv)
{
 assert(!curl_global_init(CURL_GLOBAL_DEFAULT));
 char err[256];
 if(argc==5&&!strcmp(argv[1],"parallel")) {
  upgrade_job job={.direct=1};vr_config_default(&job.config);int fd=open(argv[3],O_RDWR|O_CREAT|O_TRUNC,0600);assert(fd>=0);
  int rc=fetch_parallel(&job,argv[2],fd,strtoll(argv[4],NULL,10));close(fd);return rc?1:0;
 }
 if(argc==5&&!strcmp(argv[1],"unpack"))return vr_upgrade_unpack(argv[2],argv[3],argv[4],err,sizeof(err))?1:0;
 if(argc==5&&!strcmp(argv[1],"download")){
  upgrade_job job={.direct=1};vr_config_default(&job.config);
  int fd=open(argv[3],O_RDWR|O_CREAT|O_TRUNC,0600);assert(fd>=0);
  int64_t bytes=strtoll(argv[4],NULL,10);int complete=0;
  for(int i=0;i<3;i++){
   transfer t={.job=&job,.fd=fd,.offset=lseek(fd,0,SEEK_END),.size=bytes};
   CURLcode rc=fetch(&job,argv[2],&t);
   if(rc==CURLE_OK&&lseek(fd,0,SEEK_END)==bytes){complete=1;break;}
  }
  close(fd);return complete?0:1;
 }
 assert(argc==1);
 assert(vr_upgrade_version_newer("v0.4.70","v0.4.69-dev"));
 assert(!vr_upgrade_version_newer("v0.4.69","v0.4.69"));
 assert(!vr_upgrade_version_newer("v0.4.68","v0.4.69"));
 assert(!vr_upgrade_version_newer("v0.4.70-rc1","v0.4.69"));
 assert(!vr_upgrade_version_newer("v9999999999.0.0","v0.4.69"));
 json_object *release=json_tokener_parse("{\"tag_name\":\"v0.4.70\",\"assets\":[{\"name\":\"cloudhelper-asusbe86u-jffs.tar.gz\",\"size\":4096,\"browser_download_url\":\"https://github.com/fengzhanhuaer/CloudHelper/releases/download/v0.4.70/cloudhelper-asusbe86u-jffs.tar.gz\",\"digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}]}");
 json_object *selected=vr_upgrade_select(release,"be86u","v0.4.69",err,sizeof(err));assert(selected);json_object_put(selected);
 assert(!vr_upgrade_select(release,"ax56uv2","v0.4.69",err,sizeof(err)));
 assert(!vr_upgrade_select(release,"other","v0.4.69",err,sizeof(err)));
 json_object *asset=json_object_array_get_idx(vr_member(release,"assets"),0);
 json_object_object_add(asset,"digest",json_object_new_string(""));
 assert(!vr_upgrade_select(release,"be86u","v0.4.69",err,sizeof(err)));
 json_object_put(release);curl_global_cleanup();
 puts("PASS: stable upgrade versions, exact model package and required release SHA256");return 0;
}
