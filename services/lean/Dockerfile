FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/lean services/lean
WORKDIR /src/services/lean
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/lean ./cmd/lean

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lean /lean
ENTRYPOINT ["/lean"]
