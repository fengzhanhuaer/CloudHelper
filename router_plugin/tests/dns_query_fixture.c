#include "chvr.h"
#include <stdio.h>
#include <string.h>
#include <curl/curl.h>

int main(int argc,char **argv)
{
    if(argc!=2)return 2;
    vr_config c;char error[256],ip[16];
    if(vr_config_load(&c,argv[1],error,sizeof(error)))return 3;
    curl_global_init(CURL_GLOBAL_DEFAULT);
    int rc=vr_resolve_config4("example.com",&c,"",ip);
    curl_global_cleanup();
    if(rc)return 4;
    puts(ip);return strcmp(ip,"203.0.113.5")?5:0;
}
