ARG GO_IMAGE=golang:1.26.4-alpine
ARG RUNTIME_IMAGE=alpine:3.21

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY packages/e2m-contracts/ ./packages/e2m-contracts/
COPY app/e2m-core/ ./app/e2m-core/
WORKDIR /src/app/e2m-core
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/e2m-supply-gateway ./cmd/e2m-supply-gateway

FROM ${RUNTIME_IMAGE}
RUN addgroup -S e2m && adduser -S -G e2m e2m && mkdir /data && chown e2m:e2m /data && chmod 0700 /data
USER e2m
COPY --from=build /out/e2m-supply-gateway /usr/local/bin/e2m-supply-gateway
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/e2m-supply-gateway"]
