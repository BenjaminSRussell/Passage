# 1.26.0 includes the stdlib fix for CVE-2025-68121. Do not drop back to 1.22.
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
