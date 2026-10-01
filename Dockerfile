FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /kontrolplane

COPY go.mod go.sum ./
RUN go mod download

COPY main.go .
COPY cmd/ cmd/
COPY pkg/ pkg/

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/stdout .

FROM alpine:3.24
COPY --from=build /out/stdout /usr/local/bin/stdout
RUN adduser -D -H -u 10001 stdout
USER stdout
ENTRYPOINT ["/usr/local/bin/stdout"]
