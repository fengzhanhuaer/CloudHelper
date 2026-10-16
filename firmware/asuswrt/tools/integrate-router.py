#!/usr/bin/env python3
"""Generate a reviewable patch from the locked official tree, not live edits."""
import argparse
import difflib
from pathlib import Path

BRIDGE = r'''
/* CloudHelper: authenticated LAN-only proxy to a root-owned Unix socket.
 * Dedicated input handler avoids vendor HTTPD_DBG logging node credentials. */
static char chvr_body[16385];
static int chvr_length;
static void chvr_input(char *url, FILE *stream, int len, char *boundary)
{
    int i, c;
    memset(chvr_body, 0, sizeof(chvr_body));
    chvr_length = 0;
    if (len <= 0 || len > 16384) {
        while (len-- > 0 && (c=fgetc(stream)) != EOF) {}
        return;
    }
    for (i=0; i<len; ++i) {
        c=fgetc(stream);
        if (c==EOF || c==0) { memset(chvr_body,0,sizeof(chvr_body)); return; }
        chvr_body[i]=(char)c;
    }
    chvr_length=len;
}
static void chvr_output(char *url, FILE *stream)
{
    int fd=-1, n, total=0, header_done=0;
    FILE *local=NULL;
    struct sockaddr_un sa;
    struct sockaddr_in peer;
    socklen_t peer_len=sizeof(peer);
    struct in_addr lan, mask;
    struct timeval timeout={8,0};
    char line[4096];
    int client_fd=fileno(stream);
#ifdef RTCONFIG_HTTPS
    extern int do_ssl;
    extern int ssl_stream_fd;
    if (do_ssl) client_fd=ssl_stream_fd;
#endif
    memset(&peer,0,sizeof(peer));
    if (!chvr_length || getpeername(client_fd,(struct sockaddr *)&peer,&peer_len)<0 ||
        peer.sin_family!=AF_INET || !inet_aton(nvram_safe_get("lan_ipaddr"),&lan) ||
        !inet_aton(nvram_safe_get("lan_netmask"),&mask) ||
        (peer.sin_addr.s_addr & mask.s_addr)!=(lan.s_addr & mask.s_addr)) goto fail;
    fd=socket(AF_UNIX,SOCK_STREAM,0);
    if (fd<0) goto fail;
    setsockopt(fd,SOL_SOCKET,SO_RCVTIMEO,&timeout,sizeof(timeout));
    setsockopt(fd,SOL_SOCKET,SO_SNDTIMEO,&timeout,sizeof(timeout));
    memset(&sa,0,sizeof(sa)); sa.sun_family=AF_UNIX;
    strlcpy(sa.sun_path,"/var/run/cloudhelper-router/admin.sock",sizeof(sa.sun_path));
    if (connect(fd,(struct sockaddr *)&sa,sizeof(sa))<0) goto fail;
    local=fdopen(fd,"r+"); if (!local) goto fail;
    fprintf(local,"POST / HTTP/1.0\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",chvr_length);
    if (fwrite(chvr_body,1,chvr_length,local)!=(size_t)chvr_length || fflush(local)!=0) goto fail;
    memset(chvr_body,0,sizeof(chvr_body));chvr_length=0;
    while (fgets(line,sizeof(line),local)) {
        total+=strlen(line);
        if (total>16384) goto fail;
        if (!strcmp(line,"\r\n") || !strcmp(line,"\n")) {header_done=1;break;}
    }
    if (!header_done) goto fail;
    total=0;
    while ((n=fread(line,1,sizeof(line),local))>0) {
        total+=n;if (total>524288) break;
        fwrite(line,1,n,stream);
    }
    fclose(local);return;
fail:
    if (local) fclose(local); else if (fd>=0) close(fd);
    memset(chvr_body,0,sizeof(chvr_body));chvr_length=0;
    fputs("{\"ok\":false,\"error\":\"LAN management service unavailable\"}",stream);
}
'''

def replace_once(text, old, new):
    if text.count(old) != 1:
        raise ValueError(f'Expected one patch anchor: {old[:80]!r}')
    return text.replace(old, new, 1)

def generate(source: Path, output: Path):
    changes = {}
    def edit(relative, transform):
        old=(source/relative).read_text(encoding='utf-8-sig')
        changes[relative]=(old,transform(old))
    prefix='release/src/router/'
    def web(text):
        text=replace_once(text,'#include <sys/socket.h>','#include <sys/socket.h>\n#include <sys/un.h>')
        text=replace_once(text,'//2008.08 magic{\nstruct mime_handler mime_handlers[] =',BRIDGE+'\n//2008.08 magic{\nstruct mime_handler mime_handlers[] =')
        return replace_once(text,'struct mime_handler mime_handlers[] =\n{','struct mime_handler mime_handlers[] =\n{\n\t{ "cloudhelper_router.cgi", "application/json", no_cache_IE7, chvr_input, chvr_output, do_auth },')
    edit(prefix+'httpd/web.c',web)
    def menu(text):
        anchor='\t\t\t}\n\t\t],\n\n\t\texclude:'
        return replace_once(text,anchor,'\t\t\t},\n\t\t\t{menuName: "虚拟路由", index: "menu_VirtualRouter", tab: [{url: "VirtualRouter.asp", tabName: "虚拟路由"}]}\n\t\t],\n\n\t\texclude:')
    edit(prefix+'www/require/modules/menuTree.js',menu)
    def services(text):
        text=replace_once(text,'start_services(void)\n{','start_services(void)\n{\n\t/* Starts management even when routing is disabled. */\n\teval("/usr/sbin/cloudhelper-router-service", "start");')
        text=replace_once(text,'stop_services(void)\n{','stop_services(void)\n{\n\teval("/usr/sbin/cloudhelper-router-service", "stop");')
        text=replace_once(text,'void check_services(void)\n{','void check_services(void)\n{\n\teval("/usr/sbin/cloudhelper-router-service", "ensure");')
        text=replace_once(text,'\t\tdmservers, 1500,','\t\tf_exists("/tmp/cloudhelper-dns.servers") ? "/tmp/cloudhelper-dns.servers" : dmservers, 1500,')
        return text
    edit(prefix+'rc/services.c',services)
    def build(text):
        text=replace_once(text,'obj-y += shared','obj-y += shared\nobj-y += cloudhelper-router')
        # Vendor reinstall flattens packages in obj-y order. Install the page
        # with the www package, otherwise its later flattening moves our page
        # from /www to the filesystem root.
        anchor='\t@$(MAKE) -C www install INSTALLDIR=$(INSTALLDIR)/www TOMATO_EXPERIMENTAL=$(TOMATO_EXPERIMENTAL)'
        text=replace_once(text,anchor,anchor+'\n\tinstall -D -m 644 cloudhelper-router/VirtualRouter.asp $(INSTALLDIR)/www/www/VirtualRouter.asp')
        return text+'''

# Produced from this repository's C sources by build-router-firmware.sh.
.PHONY: cloudhelper-router cloudhelper-router-install cloudhelper-router-clean
cloudhelper-router:
	test -s cloudhelper-router/cloudhelper-router-plugin
	test -s cloudhelper-router/build-manifest.json
cloudhelper-router-install: cloudhelper-router
	install -D -m 755 cloudhelper-router/cloudhelper-router-plugin $(INSTALLDIR)/cloudhelper-router/usr/sbin/cloudhelper-router-plugin
	install -D -m 755 cloudhelper-router/cloudhelper-router-service $(INSTALLDIR)/cloudhelper-router/usr/sbin/cloudhelper-router-service
	install -D -m 644 cloudhelper-router/build-manifest.json $(INSTALLDIR)/cloudhelper-router/etc/cloudhelper-router-build.json
cloudhelper-router-clean:
	@true
'''
    edit(prefix+'Makefile',build)
    chunks=[]
    for path,(old,new) in changes.items():
        chunks.extend(difflib.unified_diff(old.splitlines(True),new.splitlines(True),fromfile='a/'+path,tofile='b/'+path))
    output.parent.mkdir(parents=True,exist_ok=True)
    output.write_bytes(''.join(chunks).encode('utf-8'))

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('source',type=Path);p.add_argument('output',type=Path);a=p.parse_args();generate(a.source,a.output)
