ARG GO_IMAGE=golang:1.26.4-alpine
ARG RUNTIME_IMAGE=alpine:3.21

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.work ./
COPY app/e2m-core/go.mod app/e2m-core/go.sum app/e2m-core/
COPY app/e2m-agent/go.mod app/e2m-agent/go.mod
COPY packages/e2m-contracts/go.mod packages/e2m-contracts/go.mod
COPY packages/e2m-contracts packages/e2m-contracts
COPY app/e2m-core app/e2m-core
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/mock-openai ./app/e2m-core/cmd/mock-openai

FROM ${RUNTIME_IMAGE}
RUN addgroup -S e2m && adduser -S -G e2m e2m
USER e2m
EXPOSE 8093
ENV MOCK_OPENAI_ADDR=:8093
COPY --from=build /out/mock-openai /usr/local/bin/mock-openai
ENTRYPOINT ["/usr/local/bin/mock-openai"]
