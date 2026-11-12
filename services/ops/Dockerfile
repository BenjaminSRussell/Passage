FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/ops services/ops
WORKDIR /src/services/ops
# FIPS variant (#599): docker build --build-arg GOFIPS140=v1.0.0 links the validated Go cryptographic module.
ARG GOFIPS140=off
RUN CGO_ENABLED=0 GOFIPS140=$GOFIPS140 go build -trimpath -ldflags="-s -w -buildid=" -o /out/ops ./cmd/ops \
 && CGO_ENABLED=0 GOFIPS140=$GOFIPS140 go build -trimpath -ldflags="-s -w -buildid=" -o /out/passage-ops ./cmd/passage-ops

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ops /ops
COPY --from=build /out/passage-ops /passage-ops
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/ops"]
