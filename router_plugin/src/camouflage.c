#define _GNU_SOURCE
#include "transport.h"
#include <stdio.h>
#include <string.h>

int vr_http_camouflage(SSL *ssl,const char *request,uint64_t deadline)
{
    char method[16],path[2048],protocol[16];
    if(sscanf(request,"%15s %2047s %15s",method,path,protocol)!=3||strncmp(protocol,"HTTP/1.",7))return -1;
    char *query=strchr(path,'?');if(query)*query=0;
    if(!strcmp(path,VR_RELAY_PATH))return 0; /* Private carrier authentication stays separate. */
    int code=404;const char *reason="Not Found",*body="404 page not found\n",*type="text/plain; charset=utf-8";
    json_object *payload=NULL;
    if(!strcmp(path,"/")) {
        payload=json_object_new_object();
        if(!strcmp(method,"GET")) {
            code=200;reason="OK";char timestamp[32];time_t now=time(NULL);struct tm utc;gmtime_r(&now,&utc);strftime(timestamp,sizeof(timestamp),"%Y-%m-%dT%H:%M:%SZ",&utc);
            json_object_object_add(payload,"message",json_object_new_string("OpenAI-compatible API endpoint"));
            json_object_object_add(payload,"api_base",json_object_new_string("/v1"));
            json_object_object_add(payload,"version",json_object_new_string(VR_VERSION));
            json_object_object_add(payload,"timestamp",json_object_new_string(timestamp));
        } else {
            code=405;reason="Method Not Allowed";json_object *error=json_object_new_object();char message[96];snprintf(message,sizeof(message),"Method %s is not allowed for this endpoint.",method);
            json_object_object_add(error,"message",json_object_new_string(message));
            json_object_object_add(error,"type",json_object_new_string("invalid_request_error"));
            json_object_object_add(error,"param",NULL);json_object_object_add(error,"code",json_object_new_string("method_not_allowed"));json_object_object_add(payload,"error",error);
        }
    } else if(!strcmp(path,"/v1")||!strncmp(path,"/v1/",4)) {
        code=401;reason="Unauthorized";
        const char *unauthorized="{\"error\":{\"message\":\"Incorrect API key provided.\",\"type\":\"invalid_request_error\",\"param\":null,\"code\":\"invalid_api_key\"}}";
        payload=vr_json_parse(unauthorized,strlen(unauthorized));
        /* Keep the fixed response independent from client-supplied credentials. */
        if(!payload)payload=json_object_new_object();
    }
    if(payload){body=json_object_to_json_string_ext(payload,JSON_C_TO_STRING_PLAIN);type="application/json; charset=utf-8";}
    char header[320];size_t n=strlen(body);int length=snprintf(header,sizeof(header),"HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %zu\r\nConnection: close\r\n%s\r\n",code,reason,type,n,code==401?"WWW-Authenticate: Bearer\r\n":"");
    int rc=length<0||length>=(int)sizeof(header)||vr_tls_io(ssl,1,header,(size_t)length,deadline)||
        (strcmp(method,"HEAD")&&vr_tls_io(ssl,1,(void *)body,n,deadline));
    json_object_put(payload);
    if(!rc)SSL_shutdown(ssl); /* Send close_notify for curl/Schannel. */
    return rc?-1:1;
}
