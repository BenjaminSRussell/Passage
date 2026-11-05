FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/ledger services/ledger
WORKDIR /src/services/ledger
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/ledger ./cmd/ledger

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ledger /ledger
ENTRYPOINT ["/ledger"]
