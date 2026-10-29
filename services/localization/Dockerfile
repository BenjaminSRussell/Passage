FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/localization services/localization
WORKDIR /src/services/localization
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/localization ./cmd/localization

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/localization /localization
# Packs are metadata shipped with the image; the service refuses to start if any fails validation.
COPY countrypacks /countrypacks
ENV COUNTRY_PACKS_DIR=/countrypacks
ENTRYPOINT ["/localization"]
