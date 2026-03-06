FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/nexora-pay ./cmd/server

FROM alpine:3.21

RUN addgroup -S app && adduser -S app -G app && apk add --no-cache ca-certificates
WORKDIR /app

COPY --from=builder /out/nexora-pay /usr/local/bin/nexora-pay

EXPOSE 8082
USER app

ENTRYPOINT ["/usr/local/bin/nexora-pay"]
