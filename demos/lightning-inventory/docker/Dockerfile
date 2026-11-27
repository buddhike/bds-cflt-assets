# The image of the Lightning inventory Launchpad demo. Build from demos/lightning-inventory:
#
#   docker build -f docker/Dockerfile -t lightning-inventory .
#
# One image runs all three ECS services (control, producer, query): LIGHTNING_ROLE picks.

# The Svelte app, which the server binary embeds.
FROM node:22-alpine AS ui
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# Pure Go: franz-go is the Kafka client, so there is no cgo and no librdkafka, and the
# binary is static.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Nothing but the binary and the CA certificates it needs to reach Confluent Cloud and AWS.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
