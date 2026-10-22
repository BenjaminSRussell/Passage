# The image build uses Go 1.26.0 so the binary includes the stdlib fix for CVE-2025-68121.
# go.mod and CI stay on Go 1.22, which matches the host toolchain used for unit tests.
FROM golang:1.26.0-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/hello-ledger ./cmd/hello-ledger

FROM scratch
COPY --from=build /out/hello-ledger /hello-ledger
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hello-ledger"]
