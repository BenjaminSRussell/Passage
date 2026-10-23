FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/platform-health services/platform-health
WORKDIR /src/services/platform-health
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/hello-ledger ./cmd/hello-ledger

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hello-ledger /hello-ledger
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/hello-ledger"]
