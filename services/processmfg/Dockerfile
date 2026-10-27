FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/processmfg services/processmfg
WORKDIR /src/services/processmfg
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/processmfg ./cmd/processmfg

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/processmfg /processmfg
ENTRYPOINT ["/processmfg"]
