FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/tax services/tax
WORKDIR /src/services/tax
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/tax ./cmd/tax

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tax /tax
ENTRYPOINT ["/tax"]
