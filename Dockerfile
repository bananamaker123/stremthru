# === Build stage ===
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git build-base

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Enable CGo and fts5 tag for SQLite full-text search
RUN CGO_ENABLED=1 go build --tags 'fts5' -o stremthru .

# === Runtime stage ===
FROM alpine:latest

RUN apk add --no-cache git ffmpeg

WORKDIR /app

COPY --from=builder /app/stremthru .

RUN mkdir -p /app/data && chmod 755 /app/data

EXPOSE 8080

CMD ["./stremthru"]
