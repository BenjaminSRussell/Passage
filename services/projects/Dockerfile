FROM golang:1.23-alpine AS build
WORKDIR /src
COPY platform/go platform/go
COPY services/projects services/projects
WORKDIR /src/services/projects
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/projects ./cmd/projects

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/projects /projects
ENTRYPOINT ["/projects"]
