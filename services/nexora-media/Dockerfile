FROM node:20-alpine

WORKDIR /app

COPY package.json ./
RUN npm install --omit=dev

COPY . .

RUN addgroup -S app && adduser -S app -G app \
  && mkdir -p /data \
  && chown -R app:app /app /data

USER app

EXPOSE 8085
ENV PORT=8085

ENTRYPOINT ["node", "server.js"]
