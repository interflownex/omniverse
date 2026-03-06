FROM node:20-alpine

WORKDIR /app

RUN addgroup -S app && adduser -S app -G app \
  && mkdir -p /data \
  && chown app:app /app /data

USER app

COPY --chown=app:app package.json ./
RUN npm install --omit=dev

COPY --chown=app:app . .

EXPOSE 8085
ENV PORT=8085

ENTRYPOINT ["node", "server.js"]
