#define _GNU_SOURCE
#include "transport.h"
#include "platform.h"
#include <assert.h>
#include <arpa/inet.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

int main(int argc,char **argv)
{
    assert(!curl_global_init(CURL_GLOBAL_DEFAULT));
    signal(SIGPIPE,SIG_IGN);
    if(argc==3&&!strcmp(argv[1],"--resolve")) {
        vr_config c;vr_config_default(&c);c.doh_enabled=c.dot_enabled=0;c.plain_dns_enabled=1;assert(!vr_copy(c.dns_upstream,sizeof(c.dns_upstream),argv[2]));
        char ips[4][46];int count=vr_resolve_transport("carrier.fixture",&c,NULL,ips);
        assert(count==2&&!strcmp(ips[0],"192.0.2.1")&&!strcmp(ips[1],"2001:db8::1"));
        puts("PASS real transport A/AAAA DNS and interleaved candidates");return 0;
    }
    if(argc==3&&!strcmp(argv[1],"--controller")) {
        vr_config c;vr_config_default(&c);snprintf(c.controller,sizeof(c.controller),"%s",argv[2]);strcpy(c.node_id,"7");strcpy(c.node_key,"fixture-key");
        vr_controller_link_init(&c,"/tmp/chvr-transport-fixture-config.json");
        char error[256];json_object *o=vr_controller_call(&c,"missing-v4-wan","GET","/api/probe/route/config",NULL,error,sizeof(error));
        if(!o){fprintf(stderr,"%s\n",error);}assert(o&&json_object_get_boolean(vr_member(o,"ok")));json_object_put(o);puts("PASS IPv6 authenticated controller HTTP");return 0;
    }
    if(argc==5&&!strcmp(argv[1],"--server")) {
        char cert[8192],key[8192];size_t n;
        assert(!vr_read_file(argv[2],cert,sizeof(cert)-1,&n));cert[n]=0;
        assert(!vr_read_file(argv[3],key,sizeof(key)-1,&n));key[n]=0;
        SSL_CTX *ctx=vr_tls_server_context(cert,key);assert(ctx);
        int listener=vr_bind6_carrier(atoi(argv[4]));assert(listener>=0);puts("READY");fflush(stdout);
        struct pollfd p={listener,POLLIN,0};assert(poll(&p,1,5000)>0);
        int fd=accept4(listener,NULL,NULL,SOCK_CLOEXEC);assert(fd>=0);
        SSL *ssl=vr_tls_accept(ctx,fd,vr_millis()+3000);assert(ssl);char header[1024];assert(!vr_http_header(ssl,header,sizeof(header),vr_millis()+1000));
        const char *response="HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n";assert(!vr_tls_io(ssl,1,(void *)response,strlen(response),vr_millis()+1000));
        SSL_free(ssl);SSL_CTX_free(ctx);close(fd);close(listener);puts("PASS IPv6 carrier TLS accept");return 0;
    }
    char host[254],text[384];int port,tls;
    assert(!vr_origin("https://[2001:db8::1]:15030",host,&port,&tls)&&!strcmp(host,"2001:db8::1")&&port==15030&&tls);
    assert(!vr_origin("https://[::1]/",host,&port,&tls)&&port==443);
    assert(vr_origin("https://2001:db8::1:15030",host,&port,&tls));
    assert(vr_origin("https://[::1]@evil.example",host,&port,&tls));
    assert(vr_origin("https://[::1%lo]:15030",host,&port,&tls));
    assert(!vr_hostport("2001:db8::1",12040,text,sizeof(text))&&!strcmp(text,"[2001:db8::1]:12040"));
    assert(!vr_curl_address("carrier.example",12040,"2001:db8::1",text,sizeof(text))&&!strcmp(text,"carrier.example:12040:[2001:db8::1]"));
    assert(!vr_curl_address("2001:db8::1",12040,"2001:db8::1",text,sizeof(text))&&!*text);
    vr_config c;vr_config_default(&c);char ips[4][46];assert(vr_resolve_transport("::1",&c,NULL,ips)==1&&!strcmp(ips[0],"::1"));
    uint8_t reply[]={0x12,0x34,0x81,0x80,0,1,0,1,0,0,0,0,1,'x',0,0,28,0,1,0xc0,0x0c,0,28,0,1,0,0,0,60,0,16,0x20,1,0x0d,0xb8,0,0,0,0,0,0,0,0,0,0,0,1};
    assert(vr_dns_addresses6(reply,sizeof(reply),ips)==1&&!strcmp(ips[0],"2001:db8::1"));
    assert(vr_dns_addresses6(reply,sizeof(reply)-1,ips)<0);
    /* Discovery retains both families; IPv6 endpoint formatting and cooldown. */
    snprintf(c.controller,sizeof(c.controller),"https://controller.example");char config[128];snprintf(config,sizeof(config),"/tmp/chvr-transport-%ld/config.json",(long)getpid());
    char dir[128];snprintf(dir,sizeof(dir),"/tmp/chvr-transport-%ld",(long)getpid());assert(!mkdir(dir,0700));vr_controller_link_init(&c,config);
    json_object *data=json_object_new_object(),*items=json_object_new_array();
    json_object_object_add(data,"version",json_object_new_int(1));json_object_object_add(data,"expires_at",json_object_new_int64(time(NULL)+3600));
    const char *addresses[]={"2001:db8::1","192.0.2.1"};
    for(int i=0;i<2;i++){json_object *item=json_object_new_object();json_object_object_add(item,"ip",json_object_new_string(addresses[i]));json_object_object_add(item,"port",json_object_new_int(15030));json_object_object_add(item,"tls_spki_sha256",json_object_new_string("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"));json_object_array_add(items,item);}
    json_object_object_add(data,"endpoints",items);assert(!vr_controller_link_store(&c,data));json_object_put(data);
    char origin[1024],pin[65];vr_controller_link_feedback(&c,0,0,3000);assert(vr_controller_link_target(&c,origin,pin)&&!strcmp(origin,"https://192.0.2.1:15030"));
    vr_controller_link_feedback(&c,1,0,10);assert(vr_controller_link_target(&c,origin,pin)&&!strcmp(origin,"https://[2001:db8::1]:15030"));
    vr_controller_link_feedback(&c,1,0,10);vr_controller_link_feedback(&c,0,0,3000);assert(!vr_controller_link_target(&c,origin,pin));
    snprintf(text,sizeof(text),"%s/controller-direct.json",dir);unlink(text);rmdir(dir);
    puts("PASS transport IPv6 origins, DNS bounds, formatting, literal bypass, discovery and cooldown");return 0;
}
