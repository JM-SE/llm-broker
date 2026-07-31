# syntax=docker/dockerfile:1
# Multi-stage build: reproducible, non-root, minimal runtime. The image
# contains the single binary and the Apache-2.0 license text; no source,
# build cache, or secrets.

FROM golang:1.26 AS build
WORKDIR /src

ARG VERSION=0.1.0
ARG COMMIT=unknown
ARG BUILDTIME=unknown

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/JM-SE/llm-broker/internal/version.Version=${VERSION} \
      -X github.com/JM-SE/llm-broker/internal/version.Commit=${COMMIT} \
      -X github.com/JM-SE/llm-broker/internal/version.BuildTime=${BUILDTIME}" \
    -o /out/llm-broker ./cmd/llm-broker

FROM alpine:3.22
RUN adduser -D -u 10001 llm-broker
COPY --from=build /out/llm-broker /usr/local/bin/llm-broker
COPY LICENSE /licenses/LICENSE
USER llm-broker
ENTRYPOINT ["/usr/local/bin/llm-broker"]
