FROM alpine:3.20

WORKDIR /opt/cloudhelper/probe_exit_node

COPY docker-entrypoint.sh /usr/local/bin/cloudhelper-probe-exit-node-entrypoint
COPY mihomo-LICENSE /usr/share/licenses/mihomo/LICENSE
RUN apk add --no-cache ca-certificates curl gzip jq \
    && chmod +x /usr/local/bin/cloudhelper-probe-exit-node-entrypoint \
    && mkdir -p /opt/cloudhelper/probe_exit_node/program /opt/cloudhelper/probe_exit_node/data /opt/cloudhelper/probe_exit_node/log /opt/cloudhelper/probe_exit_node/temp

ENV PROBE_NODE_DATA_DIR=/opt/cloudhelper/probe_exit_node/data
ENV PROBE_EXIT_NODE_BIN=/opt/cloudhelper/probe_exit_node/program/probe_exit_node
ENV PROBE_MIHOMO_BINARY=/opt/cloudhelper/probe_exit_node/data/mihomo
ENV RELEASE_REPO=fengzhanhuaer/CloudHelper
ENV RELEASE_TAG=latest

VOLUME ["/opt/cloudhelper/probe_exit_node/program", "/opt/cloudhelper/probe_exit_node/data", "/opt/cloudhelper/probe_exit_node/log", "/opt/cloudhelper/probe_exit_node/temp"]

ENTRYPOINT ["/usr/local/bin/cloudhelper-probe-exit-node-entrypoint"]
