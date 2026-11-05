FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/receiving services/receiving
WORKDIR /src/services/receiving
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/receiving ./cmd/receiving

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/receiving /receiving
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/receiving"]
