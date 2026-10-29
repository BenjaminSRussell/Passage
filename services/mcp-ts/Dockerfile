# Built from the repository root: docker build -f services/mcp-ts/Dockerfile .
FROM node:22-alpine AS build
WORKDIR /src
COPY services/mcp-ts/package.json services/mcp-ts/package-lock.json ./
RUN npm ci --silent
COPY services/mcp-ts/tsconfig.json services/mcp-ts/tsconfig.build.json ./
COPY services/mcp-ts/src src
RUN npm run build

# No runtime dependencies: the image holds the compiled gateway and package.json
# ("type": "module") only. Distroless runs as the numeric nonroot user 65532,
# which the chart's runAsNonRoot requires.
FROM gcr.io/distroless/nodejs22-debian12:nonroot
WORKDIR /app
COPY services/mcp-ts/package.json package.json
COPY --from=build /src/dist dist
EXPOSE 8080 9090
CMD ["dist/main.js"]
