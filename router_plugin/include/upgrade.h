#ifndef CHVR_UPGRADE_H
#define CHVR_UPGRADE_H
#include "service.h"
#define VR_UPGRADE_PACKAGE_MAX (2u*1024u*1024u)
const char *vr_upgrade_profile(void);
int vr_upgrade_version_newer(const char *latest,const char *current);
json_object *vr_upgrade_select(json_object *release,const char *profile,const char *current,char *err,size_t n);
int vr_upgrade_unpack(const char *archive,const char *directory,const char *profile,char *err,size_t n);
int vr_upgrade_start(vr_service *s,json_object *request,char *err,size_t n);
void vr_upgrade_join(vr_service *s);
json_object *vr_upgrade_status(vr_service *s);
int vr_upgrade_apply(const char *directory);
#endif
