FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/purchasing services/purchasing
WORKDIR /src/services/purchasing
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/purchasing ./cmd/purchasing

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/purchasing /purchasing
ENTRYPOINT ["/purchasing"]
