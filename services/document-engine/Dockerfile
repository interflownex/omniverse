FROM node:20-alpine AS deps

WORKDIR /app
COPY package.json ./
RUN npm install --omit=dev --no-audit --no-fund \
  && npm cache clean --force

FROM node:20-alpine

RUN addgroup -S app && adduser -S app -G app \
  && mkdir -p /app /data \
  && chown -R app:app /app /data

WORKDIR /app

COPY --from=deps --chown=app:app /app/node_modules ./node_modules
COPY --chown=app:app package.json server.js ./

USER app

EXPOSE 8094
ENV PORT=8094

ENTRYPOINT ["node", "server.js"]
