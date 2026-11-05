FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/scheduling services/scheduling
WORKDIR /src/services/scheduling
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/scheduling ./cmd/scheduling

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/scheduling /scheduling
ENTRYPOINT ["/scheduling"]
