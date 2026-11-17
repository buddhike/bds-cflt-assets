# Kafka Connect worker with the Debezium MySQL connector. The Confluent image
# already ships the Avro converter and Schema Registry client.
FROM alpine:3.22 AS plugins
ARG DEBEZIUM_VERSION=3.6.4.Final
ARG DEBEZIUM_SHA256=5f15c05f734a363d69bea30154c605c09b528aba4f93a3a32bbbf37c0c740e8d
RUN apk add --no-cache curl \
 && curl -fsSL -o /tmp/debezium.tgz \
      "https://repo1.maven.org/maven2/io/debezium/debezium-connector-mysql/${DEBEZIUM_VERSION}/debezium-connector-mysql-${DEBEZIUM_VERSION}-plugin.tar.gz" \
 && echo "${DEBEZIUM_SHA256}  /tmp/debezium.tgz" | sha256sum -c - \
 && mkdir -p /plugins \
 && tar -xzf /tmp/debezium.tgz -C /plugins

FROM confluentinc/cp-kafka-connect:8.1.7
COPY --from=plugins /plugins /usr/share/confluent-hub-components
