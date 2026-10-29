FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/compliance services/compliance
WORKDIR /src/services/compliance
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/compliance ./cmd/compliance

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/compliance /compliance
ENTRYPOINT ["/compliance"]
