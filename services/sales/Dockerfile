FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/sales services/sales
WORKDIR /src/services/sales
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/sales ./cmd/sales

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sales /sales
ENTRYPOINT ["/sales"]
