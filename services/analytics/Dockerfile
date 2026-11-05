FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/analytics services/analytics
WORKDIR /src/services/analytics
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/analytics ./cmd/analytics

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/analytics /analytics
ENTRYPOINT ["/analytics"]
