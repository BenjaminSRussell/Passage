FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/payroll services/payroll
WORKDIR /src/services/payroll
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/payroll ./cmd/payroll

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/payroll /payroll
ENTRYPOINT ["/payroll"]
