# Build from the repository root: docker build -f services/assets/Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go ./platform/go
COPY services/assets ./services/assets
WORKDIR /src/services/assets
# FIPS variant (#599): docker build --build-arg GOFIPS140=v1.0.0 links the validated Go cryptographic module.
ARG GOFIPS140=off
RUN CGO_ENABLED=0 GOFIPS140=$GOFIPS140 go build -trimpath -ldflags="-s -w" -o /out/assets ./cmd/assets

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/assets /usr/local/bin/assets
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/assets"]
