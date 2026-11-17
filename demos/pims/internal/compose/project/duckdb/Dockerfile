# DuckDB CLI with the extensions needed to read Tableflow Iceberg tables
# through the Iceberg REST catalog.
FROM debian:bookworm-slim
ARG DUCKDB_VERSION=1.5.2
ARG DUCKDB_SHA256_amd64=fc9145affabca627431e73ddaf6b8117e5c192692480c13886f227be202d5d15
ARG DUCKDB_SHA256_arm64=28b15a8d78e6df62f6ec43da6b0e6397dcd28e25ab93d847df3c5c97f59375f5
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl unzip \
 && curl -fsSL -o /tmp/duckdb.zip \
      "https://github.com/duckdb/duckdb/releases/download/v${DUCKDB_VERSION}/duckdb_cli-linux-${TARGETARCH}.zip" \
 && eval "expected=\${DUCKDB_SHA256_${TARGETARCH}}" \
 && echo "${expected}  /tmp/duckdb.zip" | sha256sum -c - \
 && unzip /tmp/duckdb.zip -d /usr/local/bin \
 && rm /tmp/duckdb.zip \
 && apt-get purge -y curl unzip && apt-get autoremove -y && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home duckdb

USER duckdb
# Pre-install extensions so each latency query does not download them.
RUN duckdb -c "INSTALL httpfs; INSTALL iceberg; INSTALL avro;"
