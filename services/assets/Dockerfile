# Build from the repository root: docker build -f services/assets/Dockerfile .
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go ./platform/go
COPY services/assets ./services/assets
WORKDIR /src/services/assets
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/assets ./cmd/assets

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/assets /usr/local/bin/assets
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/assets"]
