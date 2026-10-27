FROM golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd/ cmd/
COPY internal/ internal/

# VERSION is reported by the service. FAULT_* bake a default fault profile into
# the binary so a "bad" release is a distinct, immutable image tag.
ARG VERSION=dev
ARG FAULT_ERROR_RATE=0
ARG FAULT_LATENCY_MS=0
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.defaultErrorRate=${FAULT_ERROR_RATE} -X main.defaultLatencyMs=${FAULT_LATENCY_MS}" \
      -o /out/demo-api ./cmd/demo-api

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/ahmedelgmal310/k8s-progressive-delivery" \
      org.opencontainers.image.title="demo-api" \
      org.opencontainers.image.description="Demo service for progressive delivery with automated rollback" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /out/demo-api /demo-api
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/demo-api"]
