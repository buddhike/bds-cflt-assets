# The image of the automated API diagnosis Launchpad demo. Build from demos/automated-api-diagnosis:
#
#   docker build -f docker/Dockerfile -t automated-api-diagnosis .

# The Svelte app, which the server binary embeds.
FROM node:22-alpine AS ui
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# One binary: the simulator, the monitor and the UI. confluent-kafka-go links librdkafka
# through cgo (its Schema Registry client reads the library's version), so this is a glibc
# build with cgo, which bundles librdkafka statically, and not a CGO_ENABLED=0 one.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# glibc and nothing else: the binary needs libc, but not a shell or a package manager.
FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
