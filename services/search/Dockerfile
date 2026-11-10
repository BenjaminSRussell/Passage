FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/search services/search
WORKDIR /src/services/search
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/search ./cmd/search

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/search /search
ENTRYPOINT ["/search"]
