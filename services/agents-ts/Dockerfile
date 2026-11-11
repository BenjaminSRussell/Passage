# Built from the repository root: docker build -f services/agents-ts/Dockerfile .
FROM node:22-alpine AS build
WORKDIR /src
COPY services/agents-ts/package.json services/agents-ts/package-lock.json ./
RUN npm ci --silent
COPY services/agents-ts/tsconfig.json services/agents-ts/tsconfig.build.json ./
COPY services/agents-ts/src src
RUN npm run build
# Runtime dependencies only (pg); the compiled service and its migrations go into the final image.
FROM node:22-alpine AS deps
WORKDIR /src
COPY services/agents-ts/package.json services/agents-ts/package-lock.json ./
RUN npm ci --omit=dev --silent

# Distroless runs as the numeric nonroot user 65532, which the chart's runAsNonRoot requires.
FROM gcr.io/distroless/nodejs22-debian12:nonroot
WORKDIR /app
COPY services/agents-ts/package.json package.json
COPY --from=deps /src/node_modules node_modules
COPY --from=build /src/dist dist
COPY services/agents-ts/migrations migrations
EXPOSE 8080 9090
CMD ["dist/main.js"]
