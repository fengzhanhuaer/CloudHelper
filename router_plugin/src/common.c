#define _GNU_SOURCE
#include "chvr.h"
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/stat.h>

json_object *vr_member(json_object *o, const char *key)
{
    json_object *v = NULL;
    if (o && json_object_is_type(o, json_type_object)) json_object_object_get_ex(o, key, &v);
    return v;
}

const char *vr_string(json_object *o, const char *key)
{
    json_object *v = vr_member(o, key);
    if(!v || !json_object_is_type(v,json_type_string))return "";
    const char *s=json_object_get_string(v);
    return strlen(s)==(size_t)json_object_get_string_len(v)?s:"";
}

json_object *vr_json_parse(const void *data, size_t n)
{
    if (!data || !n || n > VR_JSON_MAX || memchr(data, 0, n)) return NULL;
    struct json_tokener *t = json_tokener_new_ex(24);
    if (!t) return NULL;
    json_tokener_set_flags(t, JSON_TOKENER_STRICT);
    json_object *o = json_tokener_parse_ex(t, data, (int)n);
    size_t used = (size_t)t->char_offset; /* Available in the bundled json-c 0.12. */
    if (json_tokener_get_error(t) != json_tokener_success) { json_object_put(o); o = NULL; }
    const unsigned char *p = data;
    while (used < n && (p[used]==' ' || p[used]=='\t' || p[used]=='\r' || p[used]=='\n')) used++;
    if (used != n) { json_object_put(o); o = NULL; }
    json_tokener_free(t);
    return o;
}

int vr_copy(char *dst, size_t cap, const char *src)
{
    size_t n = strlen(src);
    if (n >= cap) return -1;
    memcpy(dst, src, n+1);
    return 0;
}

int vr_read_file(const char *path, void *bytes, size_t cap, size_t *n)
{
    int fd = open(path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) return -1;
    size_t used = 0;
    while (used < cap) {
        ssize_t k = read(fd, (char *)bytes+used, cap-used);
        if (k < 0 && errno == EINTR) continue;
        if (k < 0) { close(fd); return -1; }
        if (!k) break;
        used += (size_t)k;
    }
    char extra;
    ssize_t k;
    do { k = read(fd, &extra, 1); } while (k < 0 && errno == EINTR);
    close(fd);
    if (k != 0) { errno=EFBIG; return -1; }
    *n=used;
    return 0;
}

int vr_atomic_write(const char *path, const void *bytes, size_t n)
{
    char tmp[1024], parent[1024];
    if (snprintf(tmp,sizeof(tmp),"%s.XXXXXX",path) >= (int)sizeof(tmp) || vr_copy(parent,sizeof(parent),path)) return -1;
    char *slash=strrchr(parent,'/');
    if (!slash) strcpy(parent,"."); else if (slash==parent) slash[1]=0; else *slash=0;
    int fd=mkstemp(tmp);
    if (fd<0) return -1;
    size_t off=0;
    int ok=fchmod(fd,0600)==0;
    while (ok && off<n) {
        ssize_t k=write(fd,(const char *)bytes+off,n-off);
        if (k<0 && errno==EINTR) continue;
        if (k<=0) {ok=0;break;}
        off+=(size_t)k;
    }
    if (ok && fsync(fd)) ok=0;
    if (close(fd)) ok=0;
    if (ok && rename(tmp,path)) ok=0;
    if (!ok) { unlink(tmp); return -1; }
    fd=open(parent,O_RDONLY|O_DIRECTORY|O_CLOEXEC);
    if (fd<0) return -1;
    int rc=fsync(fd); close(fd); return rc;
}
