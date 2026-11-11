FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/ops services/ops
WORKDIR /src/services/ops
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/ops ./cmd/ops

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ops /ops
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/ops"]
