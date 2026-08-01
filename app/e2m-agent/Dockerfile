ARG GO_IMAGE=golang:1.26.4-alpine
ARG RUNTIME_IMAGE=alpine:3.21

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src

COPY go.work ./
COPY app/e2m-core/go.mod app/e2m-core/go.mod
COPY app/e2m-agent/go.mod app/e2m-agent/go.mod
COPY packages/e2m-contracts/go.mod packages/e2m-contracts/go.mod
COPY packages/e2m-contracts packages/e2m-contracts
COPY app/e2m-agent app/e2m-agent

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/e2m-agent ./app/e2m-agent/cmd/e2m-agent

FROM ${RUNTIME_IMAGE}

RUN addgroup -S e2m \
    && adduser -S -D -H -G e2m e2m \
    && install -d -o e2m -g e2m -m 0700 /var/lib/e2m-agent

ENV E2M_CORE_URL=http://e2m-core:8080
ENV E2M_AGENT_DATA_DIR=/var/lib/e2m-agent

COPY --from=build /out/e2m-agent /usr/local/bin/e2m-agent

USER e2m
EXPOSE 18081

ENTRYPOINT ["/usr/local/bin/e2m-agent"]
