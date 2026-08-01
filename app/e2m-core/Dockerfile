ARG GO_IMAGE=golang:1.26.4-alpine
ARG NODE_IMAGE=node:24-alpine
ARG RUNTIME_IMAGE=alpine:3.21

# --- stage 1: build the web console ---
FROM ${NODE_IMAGE} AS web
WORKDIR /web

COPY web/console/package.json web/console/package-lock.json* ./
RUN npm ci

COPY web/console/ ./
RUN npm run build

# --- stage 2: build the Go binary with the embedded console ---
FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src

COPY go.work ./
COPY app/e2m-core/go.mod app/e2m-core/go.sum app/e2m-core/
COPY app/e2m-agent/go.mod app/e2m-agent/go.mod
COPY packages/e2m-contracts/go.mod packages/e2m-contracts/go.mod
COPY packages/e2m-contracts packages/e2m-contracts
COPY app/e2m-core app/e2m-core

# Drop the built SPA into the embed directory before compiling.
COPY --from=web /web/dist app/e2m-core/internal/webui/dist

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/e2m-core ./app/e2m-core/cmd/e2m-core

# --- stage 3: runtime ---
FROM ${RUNTIME_IMAGE}

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S e2m \
    && adduser -S -G e2m e2m \
    && mkdir -p /data \
    && chown e2m:e2m /data \
    && chmod 0700 /data

USER e2m
EXPOSE 8080
ENV E2M_CORE_ADDR=:8080

COPY --from=build /out/e2m-core /usr/local/bin/e2m-core

ENTRYPOINT ["/usr/local/bin/e2m-core"]
