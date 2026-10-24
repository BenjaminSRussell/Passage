FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/party services/party
WORKDIR /src/services/party
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/party ./cmd/party

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/party /party
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/party"]
