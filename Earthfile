VERSION 0.8
FROM golang:1.26-alpine
WORKDIR /kontrolplane

deps:
    COPY go.mod go.sum ./
    RUN go mod download

compile:
    FROM +deps
    COPY main.go .
    COPY cmd/ cmd/
    COPY pkg/ pkg/
    ARG VERSION=dev
    RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o build/kontrolplane/stdout .
    SAVE ARTIFACT build/kontrolplane/stdout AS LOCAL build/kontrolplane/stdout

container:
    ARG VERSION=dev
    FROM DOCKERFILE --build-arg VERSION=${VERSION} .
    ARG tag="latest"
    SAVE IMAGE ghcr.io/kontrolplane/stdout:${tag}

loki:
    LOCALLY
    ARG LOKI_ADDR="http://localhost:3100"
    RUN docker compose up -d
    RUN for i in $(seq 1 60); do curl -sf $LOKI_ADDR/ready >/dev/null 2>&1 && exit 0; sleep 1; done; echo "loki did not become ready" && exit 1

# seed pushes an hour of history from the made up services.
seed:
    LOCALLY
    ARG LOKI_ADDR="http://localhost:3100"
    RUN go run ./seed --addr $LOKI_ADDR --once

# generate keeps pushing new lines until stopped.
generate:
    LOCALLY
    ARG LOKI_ADDR="http://localhost:3100"
    ARG RATE=20
    RUN go run ./seed --addr $LOKI_ADDR --backfill 0 --rate $RATE

dev:
    LOCALLY
    ARG LOKI_ADDR="http://localhost:3100"
    WAIT
        BUILD +loki
    END
    RUN go run ./seed --addr $LOKI_ADDR --once
    RUN go build -o build/kontrolplane/stdout .

labels:
    LOCALLY
    ARG LOKI_ADDR="http://localhost:3100"
    RUN curl -s $LOKI_ADDR/loki/api/v1/labels

vhs:
    LOCALLY
    RUN vhs vhs/cassette.tape

all:
  BUILD +compile
  BUILD +container
