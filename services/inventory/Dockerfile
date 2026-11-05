FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/inventory services/inventory
WORKDIR /src/services/inventory
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/inventory ./cmd/inventory

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/inventory /inventory
ENTRYPOINT ["/inventory"]
