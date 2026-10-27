FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/costing services/costing
WORKDIR /src/services/costing
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/costing ./cmd/costing

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/costing /costing
ENTRYPOINT ["/costing"]
