FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/batch services/batch
WORKDIR /src/services/batch
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/batch ./cmd/batch

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/batch /batch
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/batch"]
