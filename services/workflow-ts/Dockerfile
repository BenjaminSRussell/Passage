FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json tsconfig.json tsconfig.build.json ./
RUN npm ci
COPY src ./src
RUN npm run build

FROM node:24-alpine
ENV NODE_ENV=production
USER node
WORKDIR /app
COPY --from=build --chown=node:node /app/package.json /app/package-lock.json ./
RUN npm ci --omit=dev
COPY --from=build --chown=node:node /app/dist ./dist
CMD ["node", "dist/worker.js"]
