FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/treasury services/treasury
WORKDIR /src/services/treasury
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/treasury ./cmd/treasury

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/treasury /treasury
ENTRYPOINT ["/treasury"]
