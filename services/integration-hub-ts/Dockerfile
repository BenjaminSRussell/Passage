FROM node:24-alpine
ENV NODE_ENV=production
WORKDIR /app
RUN chown 1000:1000 /app
# The node user, numerically, so runAsNonRoot can be verified.
USER 1000:1000
COPY --chown=node:node package.json package-lock.json ./
RUN npm ci --omit=dev
COPY --chown=node:node src ./src
COPY --chown=node:node migrations ./migrations
# Node 24 runs TypeScript directly by stripping types; there is no build step.
CMD ["node", "src/main.ts"]
