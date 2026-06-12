FROM alpine:3.20

WORKDIR /opt/cloudhelper/probe_node

COPY docker-entrypoint.sh /usr/local/bin/cloudhelper-probe-node-entrypoint
RUN chmod +x /usr/local/bin/cloudhelper-probe-node-entrypoint \
    && mkdir -p /opt/cloudhelper/probe_node/data /opt/cloudhelper/probe_node/logs /opt/cloudhelper/probe_node/temp

ENV PROBE_NODE_DATA_DIR=/opt/cloudhelper/probe_node/data
ENV PROBE_NODE_BIN=/opt/cloudhelper/probe_node/probe_node
ENV RELEASE_REPO=fengzhanhuaer/CloudHelper
ENV RELEASE_TAG=latest
ENV ASSET_NAME=cloudhelper-probe-node-linux-amd64

VOLUME ["/opt/cloudhelper/probe_node"]
EXPOSE 16030 16032 1080 8080

ENTRYPOINT ["/usr/local/bin/cloudhelper-probe-node-entrypoint"]
