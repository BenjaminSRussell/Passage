FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/close services/close
WORKDIR /src/services/close
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/close ./cmd/close

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/close /close
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/close"]
