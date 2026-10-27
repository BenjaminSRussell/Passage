FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/migration services/migration
WORKDIR /src/services/migration
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/migration ./cmd/migration

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/migration /migration
ENTRYPOINT ["/migration"]
