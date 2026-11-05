FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/engineering services/engineering
WORKDIR /src/services/engineering
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/engineering ./cmd/engineering

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/engineering /engineering
ENTRYPOINT ["/engineering"]
