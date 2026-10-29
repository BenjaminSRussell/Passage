FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY tools/migration-factory tools/migration-factory
WORKDIR /src/tools/migration-factory
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/migration-factory ./cmd/migration-factory

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/migration-factory /migration-factory
ENTRYPOINT ["/migration-factory"]
