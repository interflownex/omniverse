FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/nexora-chat ./cmd/server

FROM alpine:3.21

RUN addgroup -S app && adduser -S app -G app && apk add --no-cache ca-certificates
WORKDIR /app

COPY --from=builder /out/nexora-chat /usr/local/bin/nexora-chat
COPY --from=builder /src/web /app/web

EXPOSE 8086
USER app

ENTRYPOINT ["/usr/local/bin/nexora-chat"]
