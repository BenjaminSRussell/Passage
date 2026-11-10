FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/mobile services/mobile
WORKDIR /src/services/mobile
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/mobile ./cmd/mobile

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mobile /mobile
ENTRYPOINT ["/mobile"]
