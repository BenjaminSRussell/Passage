FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/metadata services/metadata
WORKDIR /src/services/metadata
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/metadata ./cmd/metadata

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/metadata /metadata
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/metadata"]
