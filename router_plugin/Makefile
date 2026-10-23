CC ?= cc
.DEFAULT_GOAL := all
BUILD ?= bin/native
VERSION ?= v$(or $(shell cat ../version 2>/dev/null | tr -d '\r\n'),0.0.0)-dev
CPPFLAGS += -Iinclude $(DEP_CPPFLAGS)
CPPFLAGS += -include $(BUILD)/build-version.h
CFLAGS ?= -Os -g
CFLAGS += -std=c99 -Wall -Wextra -Werror -Wformat=2 -fstack-protector-strong -ffunction-sections -fdata-sections
LDLIBS += -lcurl -lssl -lcrypto -ljson-c -lpthread
LDFLAGS += $(DEP_LDFLAGS) -Wl,--gc-sections -Wl,-z,relro,-z,now
CORE = src/log.c src/cache.c src/gateway.c src/report.c src/telemetry.c src/common.c src/config.c src/wire.c src/dns.c src/dns_upstream.c src/policy.c src/auth.c src/controller.c src/controller_link.c src/websocket.c src/tls.c src/packet.c src/tls_sniff.c
RUNTIME = src/main.c src/upgrade.c src/engine.c src/policy_update.c src/carrier.c src/dataplane.c src/mapping.c src/platform.c src/dns_service.c src/measure.c src/control.c src/tcp_proxy.c
.PHONY: all test clean FORCE
FORCE:
$(BUILD)/build-version.h: FORCE | $(BUILD)
	@printf '#define VR_VERSION "%s"\n' '$(VERSION)' > $@.new
	@cmp -s $@.new $@ && rm $@.new || mv $@.new $@
$(BUILD)/cloudhelper-router-plugin $(BUILD)/httpd-bridge.so $(BUILD)/httpd-fixture $(BUILD)/test-core $(BUILD)/test-logs $(BUILD)/test-dataplane $(BUILD)/test-platform $(BUILD)/test-dns $(BUILD)/test-control $(BUILD)/test-certificate $(BUILD)/test-carrier $(BUILD)/test-measure $(BUILD)/test-gateway $(BUILD)/test-cache $(BUILD)/test-mapping $(BUILD)/dns-query-fixture $(BUILD)/test-dns-system $(BUILD)/test-httpd-profile: $(BUILD)/build-version.h
all: $(BUILD)/cloudhelper-router-plugin $(BUILD)/httpd-bridge.so
$(BUILD)/test-telemetry: $(CORE) src/carrier.c tests/test_telemetry.c $(wildcard include/*.h) $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/carrier.c tests/test_telemetry.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-controller-link: $(CORE) tests/test_controller_link.c $(wildcard include/*.h) $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_controller_link.c $(LDFLAGS) -Wl,--wrap=vr_resolve_config4_all $(LDLIBS) -o $@
$(BUILD)/httpd-bridge.so: src/httpd_bridge.c include/httpd_layout.h include/httpd_access.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) -fPIC -shared $< $(LDFLAGS) -ldl -o $@
$(BUILD)/httpd-fixture: tests/httpd_fixture.c | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) -fno-pie $(if $(filter bin/native,$(BUILD)),-no-pie,) -rdynamic $< $(LDFLAGS) -lssl -lcrypto -o $@
$(BUILD)/test-httpd-profile: tests/test_httpd_profile.c include/httpd_profile.h include/httpd_layout.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $< $(LDFLAGS) -o $@
$(BUILD)/test-httpd-access: tests/test_httpd_access.c include/httpd_access.h $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $< $(LDFLAGS) -o $@
$(BUILD):
	mkdir -p $@
$(BUILD)/cloudhelper-router-plugin: $(CORE) $(RUNTIME) $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) $(RUNTIME) $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-core: $(CORE) tests/test_core.c include/chvr.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_core.c $(LDFLAGS) $(LDLIBS) -o $@
test: $(BUILD)/test-core
	$(BUILD)/test-core
$(BUILD)/test-logs: $(CORE) tests/test_logs.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_logs.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-dataplane: $(CORE) src/dataplane.c src/mapping.c tests/test_dataplane.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/dataplane.c src/mapping.c tests/test_dataplane.c $(LDFLAGS) -Wl,--wrap=vr_controller_call $(LDLIBS) -o $@
$(BUILD)/test-platform: $(CORE) src/dataplane.c src/mapping.c src/platform.c src/tcp_proxy.c tests/test_platform.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/dataplane.c src/mapping.c src/platform.c src/tcp_proxy.c tests/test_platform.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-dns: $(CORE) src/dns_service.c tests/test_dns_service.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_dns_service.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-control: $(CORE) src/control.c src/upgrade.c src/policy_update.c src/carrier.c src/mapping.c src/platform.c tests/test_control.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/control.c src/upgrade.c src/policy_update.c src/carrier.c src/mapping.c src/platform.c tests/test_control.c $(LDFLAGS) -Wl,--wrap=vr_resolve_config4_all -Wl,--wrap=curl_easy_perform $(LDLIBS) -o $@
$(BUILD)/test-certificate: $(CORE) tests/test_certificate.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_certificate.c $(LDFLAGS) -Wl,--wrap=vr_resolve_config4_all $(LDLIBS) -o $@
$(BUILD)/test-carrier: $(CORE) src/carrier.c src/measure.c src/platform.c src/policy_update.c src/mapping.c src/tcp_proxy.c tests/test_carrier.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/carrier.c src/measure.c src/platform.c src/policy_update.c src/mapping.c src/tcp_proxy.c tests/test_carrier.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-tx: $(CORE) src/carrier.c tests/test_tx.c $(wildcard include/*.h) $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/carrier.c tests/test_tx.c $(LDFLAGS) -Wl,--wrap=SSL_write -Wl,--wrap=SSL_get_error $(LDLIBS) -o $@
$(BUILD)/test-measure: $(CORE) src/measure.c tests/test_measure.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/measure.c tests/test_measure.c $(LDFLAGS) -Wl,--wrap=vr_millis $(LDLIBS) -o $@
$(BUILD)/test-policy-update: $(CORE) src/policy_update.c src/carrier.c src/mapping.c src/platform.c tests/test_policy_update.c $(wildcard include/*.h) $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/policy_update.c src/carrier.c src/mapping.c src/platform.c tests/test_policy_update.c $(LDFLAGS) -Wl,--wrap=vr_platform_routes_update -Wl,--wrap=vr_controller_call $(LDLIBS) -o $@
$(BUILD)/test-routes: $(CORE) src/platform.c tests/test_routes.c $(wildcard include/*.h) $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/platform.c tests/test_routes.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-tls-proxy: $(CORE) src/tcp_proxy.c tests/test_tls_proxy.c $(wildcard include/*.h) $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/tcp_proxy.c tests/test_tls_proxy.c $(LDFLAGS) -Wl,--wrap=connect -Wl,--wrap=vr_resolve_config4 $(LDLIBS) -o $@
$(BUILD)/test-tcp-close: $(CORE) src/tcp_proxy.c tests/test_tcp_close.c $(wildcard include/*.h) $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_tcp_close.c $(LDFLAGS) $(LDLIBS) -o $@
clean:
	rm -f $(BUILD)/cloudhelper-router-plugin $(BUILD)/test-core

$(BUILD)/test-upgrade: $(CORE) src/upgrade.c tests/test_upgrade.c $(wildcard include/*.h) $(BUILD)/build-version.h | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_upgrade.c $(LDFLAGS) $(LDLIBS) -o $@

$(BUILD)/test-gateway: $(CORE) tests/test_gateway.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_gateway.c $(LDFLAGS) $(LDLIBS) -o $@

$(BUILD)/test-cache: $(CORE) tests/test_cache.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_cache.c $(LDFLAGS) -Wl,--wrap=vr_controller_call $(LDLIBS) -o $@

$(BUILD)/test-mapping: $(CORE) src/mapping.c tests/test_mapping.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/mapping.c tests/test_mapping.c $(LDFLAGS) -Wl,--wrap=vr_controller_call -Wl,--wrap=time -Wl,--wrap=vr_millis -Wl,--wrap=vr_atomic_write $(LDLIBS) -o $@

$(BUILD)/dns-query-fixture: $(CORE) tests/dns_query_fixture.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/dns_query_fixture.c $(LDFLAGS) $(LDLIBS) -o $@

$(BUILD)/test-dns-system: $(CORE) tests/test_dns_system.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_dns_system.c $(LDFLAGS) -Wl,--wrap=vr_dns_exchange $(LDLIBS) -o $@
