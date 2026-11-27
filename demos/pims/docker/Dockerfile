# Images for the PIMS Launchpad demo. Build from demos/pims:
#
#   docker build -f docker/Dockerfile --target server -t pims-server .
#   docker build -f docker/Dockerfile --target duckdb -t pims-duckdb .
#
# The Kafka Connect image is internal/compose/project/connect/Dockerfile.

# The React app, which the server binary embeds.
FROM node:22-alpine AS ui
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# One static binary: the simulator, the UI and API, and the DuckDB agent.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pims-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/pims-server /pims-server
EXPOSE 8080
ENTRYPOINT ["/pims-server"]

# DuckDB CLI with the extensions that read Tableflow's Iceberg tables, run by an
# agent that lets the server execute queries here.
FROM debian:bookworm-slim AS duckdb
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
COPY --from=build /out/pims-server /usr/local/bin/pims-server
USER duckdb
# Pre-install extensions so each query does not download them.
RUN duckdb -c "INSTALL httpfs; INSTALL iceberg; INSTALL avro;"
# The agent listens on loopback only. The task's containers share a network
# namespace, so the server reaches it there.
ENV PIMS_AGENT_LISTEN=127.0.0.1:8081
ENTRYPOINT ["/usr/local/bin/pims-server", "duckdb-agent"]
