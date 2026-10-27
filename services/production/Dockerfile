FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/production services/production
WORKDIR /src/services/production
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/production ./cmd/production

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/production /production
ENTRYPOINT ["/production"]
