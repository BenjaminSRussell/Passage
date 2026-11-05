FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/expense services/expense
WORKDIR /src/services/expense
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/expense ./cmd/expense

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/expense /expense
ENTRYPOINT ["/expense"]
