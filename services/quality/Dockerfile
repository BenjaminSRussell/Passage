FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/quality services/quality
WORKDIR /src/services/quality
# FIPS variant (#599): docker build --build-arg GOFIPS140=v1.0.0 links the validated Go cryptographic module.
ARG GOFIPS140=off
RUN CGO_ENABLED=0 GOFIPS140=$GOFIPS140 go build -trimpath -ldflags="-s -w -buildid=" -o /out/quality ./cmd/quality

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/quality /quality
ENTRYPOINT ["/quality"]
