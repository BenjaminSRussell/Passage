FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/org services/org
WORKDIR /src/services/org
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/org ./cmd/org

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/org /org
ENTRYPOINT ["/org"]
