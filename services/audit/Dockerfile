FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/audit services/audit
WORKDIR /src/services/audit
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/audit ./cmd/audit

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/audit /audit
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/audit"]
