#ifndef CHVR_TRANSPORT_H
#define CHVR_TRANSPORT_H
#include "chvr.h"
#include <openssl/ssl.h>

typedef struct {
    uint8_t header[14], mask[4], control[125];
    size_t header_used, header_needed, payload_used, payload_length, message_length;
    int opcode, final, fragmented, expect_mask;
} vr_ws_decoder;
typedef int (*vr_ws_handler)(int opcode,const uint8_t *bytes,size_t n,void *arg);
int vr_ws_feed(vr_ws_decoder *d,const void *bytes,size_t n,vr_ws_handler fn,void *arg);
int vr_ws_encode(int opcode,int mask,const void *bytes,size_t n,uint8_t *out,size_t cap,size_t *length);
uint64_t vr_millis(void);
void vr_tls_error(char *out,size_t cap,const char *operation,int error);
int vr_tls_io(SSL *ssl,int write,void *bytes,size_t n,uint64_t deadline);
SSL_CTX *vr_tls_server_context(const char *cert,const char *key);
SSL *vr_tls_accept(SSL_CTX *ctx,int fd,uint64_t deadline);
SSL *vr_tls_connect(const char *host,int port,const char *pin,const vr_config *config,const char *iface,int *fd,uint64_t deadline);
SSL *vr_tls_connect_at(const char *host,const char *ip,int port,const char *pin,const char *iface,int *fd,uint64_t deadline);
int vr_http_header(SSL *ssl,char *header,size_t cap,uint64_t deadline);
const char *vr_http_field(const char *header,const char *name,char *out,size_t cap);
int vr_http_token(const char *header,const char *name,const char *token);
int vr_ws_accept_key(const char *key,char out[29]);
#endif
