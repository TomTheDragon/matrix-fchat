# Build stage. cgo is required for the SQLite driver, so a C toolchain is installed.
FROM golang:1.27-alpine3.24 AS builder

RUN apk add --no-cache git ca-certificates build-base
ENV CGO_ENABLED=1

WORKDIR /build
# Download dependencies first so they're cached between builds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# The build context is owned by another user, so allow git to read it for version info.
RUN git config --global --add safe.directory /build && ./build.sh

# Runtime stage. Alpine matches the musl libc the binary was linked against.
FROM alpine:3.24

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ca-certificates su-exec

COPY --from=builder /build/matrix-fchat /usr/bin/matrix-fchat
COPY ./docker-run.sh /docker-run.sh

VOLUME /data
WORKDIR /data
EXPOSE 29380

CMD ["/docker-run.sh"]
