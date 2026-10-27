FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/print services/print
WORKDIR /src/services/print
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/print ./cmd/print

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/print /print
ENTRYPOINT ["/print"]
