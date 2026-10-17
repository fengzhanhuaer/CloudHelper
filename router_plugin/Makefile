CC ?= cc
BUILD ?= bin/native
CPPFLAGS += -Iinclude $(DEP_CPPFLAGS)
CFLAGS ?= -Os -g
CFLAGS += -std=c99 -Wall -Wextra -Werror -Wformat=2 -fstack-protector-strong -ffunction-sections -fdata-sections
LDLIBS += -lcurl -lssl -lcrypto -ljson-c -lpthread
LDFLAGS += $(DEP_LDFLAGS) -Wl,--gc-sections -Wl,-z,relro,-z,now
CORE = src/log.c src/cache.c src/gateway.c src/report.c src/common.c src/config.c src/wire.c src/dns.c src/dns_upstream.c src/policy.c src/auth.c src/controller.c src/websocket.c src/tls.c src/packet.c
RUNTIME = src/main.c src/engine.c src/carrier.c src/dataplane.c src/mapping.c src/platform.c src/dns_service.c src/measure.c src/control.c
.PHONY: all test clean
all: $(BUILD)/cloudhelper-router-plugin $(BUILD)/httpd-bridge.so
$(BUILD)/httpd-bridge.so: src/httpd_bridge.c | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) -fPIC -shared $< $(LDFLAGS) -ldl -o $@
$(BUILD)/httpd-fixture: tests/httpd_fixture.c | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) -rdynamic $< $(LDFLAGS) -o $@
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
$(BUILD)/test-platform: $(CORE) src/dataplane.c src/mapping.c src/platform.c tests/test_platform.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/dataplane.c src/mapping.c src/platform.c tests/test_platform.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-dns: $(CORE) src/dns_service.c tests/test_dns_service.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) tests/test_dns_service.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-control: $(CORE) src/control.c tests/test_control.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/control.c tests/test_control.c $(LDFLAGS) -Wl,--wrap=vr_resolve_config4_all $(LDLIBS) -o $@
$(BUILD)/test-carrier: $(CORE) src/carrier.c src/measure.c src/platform.c tests/test_carrier.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/carrier.c src/measure.c src/platform.c tests/test_carrier.c $(LDFLAGS) $(LDLIBS) -o $@
$(BUILD)/test-measure: $(CORE) src/measure.c tests/test_measure.c $(wildcard include/*.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CFLAGS) $(CORE) src/measure.c tests/test_measure.c $(LDFLAGS) -Wl,--wrap=vr_millis $(LDLIBS) -o $@
clean:
	rm -f $(BUILD)/cloudhelper-router-plugin $(BUILD)/test-core

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
