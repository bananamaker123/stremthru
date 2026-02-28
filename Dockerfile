# === Build stage ===
# Use a small official Go image
FROM golang:1.25-alpine AS builder

# Install anything Go needs to compile
RUN apk add --no-cache git build-base

# Set working directory
WORKDIR /app

# Copy module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the binary (no cross‑compile needed)
RUN go build -o stremthru .

# === Runtime stage ===
FROM alpine:latest

# Install runtime tools
RUN apk add --no-cache git ffmpeg

WORKDIR /app

# Copy compiled binary from the builder stage
COPY --from=builder /app/stremthru .

# Expose port and set default command
EXPOSE 8080

# Ensure data directory exists
RUN mkdir -p /app/data

# Set it as a volume (optional, for persistence)
VOLUME ["/app/data"]

CMD ["./stremthru"]

