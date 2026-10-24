FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/payables services/payables
WORKDIR /src/services/payables
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/payables ./cmd/payables

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/payables /payables
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/payables"]
