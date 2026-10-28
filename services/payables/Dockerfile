FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/payables services/payables
WORKDIR /src/services/payables
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/payables ./cmd/payables

FROM alpine:3.22@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f
RUN apk add --no-cache libxml2-utils && apk del --no-cache apk-tools
COPY --from=build /out/payables /payables
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/payables"]
