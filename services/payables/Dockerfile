FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/payables services/payables
WORKDIR /src/services/payables
# FIPS variant (#599): docker build --build-arg GOFIPS140=v1.0.0 links the validated Go cryptographic module.
ARG GOFIPS140=off
RUN CGO_ENABLED=0 GOFIPS140=$GOFIPS140 go build -trimpath -ldflags="-s -w -buildid=" -o /out/payables ./cmd/payables

FROM debian:12-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3 AS xmllint
RUN apt-get update && apt-get install -y --no-install-recommends libxml2-utils \
    && mkdir -p /rootfs/usr/bin \
    && cp /usr/bin/xmllint /rootfs/usr/bin/ \
    && ldd /usr/bin/xmllint | awk '/=> \//{print $3}' | grep -E 'libxml2|libicu|libz|liblzma' \
        | xargs -I{} cp --parents -L {} /rootfs

FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=xmllint /rootfs/ /
COPY --from=build /out/payables /payables
EXPOSE 8080 9090
ENTRYPOINT ["/payables"]
