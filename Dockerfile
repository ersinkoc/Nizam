FROM golang:1.26.2-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./

ARG VERSION=0.1.0-dev
ARG COMMIT=container
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/mizanproxy/mizan/internal/version.Version=${VERSION} -X github.com/mizanproxy/mizan/internal/version.Commit=${COMMIT} -X github.com/mizanproxy/mizan/internal/version.Date=${DATE}" \
    -o /out/mizan ./cmd/mizan

FROM alpine:3.23 AS runtime-base

RUN apk add --no-cache ca-certificates \
    && addgroup -S mizan \
    && mkdir -p /var/lib/mizan \
    && adduser -S -D -h /var/lib/mizan -s /sbin/nologin -G mizan mizan \
    && chown -R mizan:mizan /var/lib/mizan

COPY --from=build /out/mizan /usr/local/bin/mizan

USER mizan
WORKDIR /var/lib/mizan
VOLUME ["/var/lib/mizan"]
EXPOSE 7890

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["mizan", "doctor", "--home", "/var/lib/mizan", "--json"]

ENTRYPOINT ["mizan"]
CMD ["serve", "--bind", "0.0.0.0:7890", "--home", "/var/lib/mizan"]

FROM runtime-base AS runtime-ssh

USER root
RUN apk add --no-cache openssh-client
USER mizan

FROM runtime-base AS runtime

USER mizan
