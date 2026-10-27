FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/planning services/planning
WORKDIR /src/services/planning
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/planning ./cmd/planning

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/planning /planning
ENTRYPOINT ["/planning"]
