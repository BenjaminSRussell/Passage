FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/numbering services/numbering
WORKDIR /src/services/numbering
# FIPS variant (#599): docker build --build-arg GOFIPS140=v1.0.0 links the validated Go cryptographic module.
ARG GOFIPS140=off
RUN CGO_ENABLED=0 GOFIPS140=$GOFIPS140 go build -trimpath -ldflags="-s -w -buildid=" -o /out/numbering ./cmd/numbering

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/numbering /numbering
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/numbering"]
