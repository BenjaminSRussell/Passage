FROM golang:1.25-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/identity services/identity
WORKDIR /src/services/identity
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/identity ./cmd/identity

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/identity /identity
COPY policies/authz/data.json policies/authz/authz.rego /etc/passage/authz/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/identity"]
