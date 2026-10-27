FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/numbering services/numbering
WORKDIR /src/services/numbering
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/numbering ./cmd/numbering

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/numbering /numbering
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/numbering"]
