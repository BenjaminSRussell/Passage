FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/fieldservice services/fieldservice
WORKDIR /src/services/fieldservice
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/fieldservice ./cmd/fieldservice

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fieldservice /fieldservice
ENTRYPOINT ["/fieldservice"]
