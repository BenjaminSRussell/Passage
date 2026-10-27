FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/fx services/fx
WORKDIR /src/services/fx
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/fx ./cmd/fx

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fx /fx
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/fx"]
