# Build stage
FROM golang:1.26-alpine AS build

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build binary
COPY . .
RUN CGO_ENABLED=0 go build -o crawler ./cmd/cli

# Runtime stage
FROM alpine:latest

WORKDIR /app

COPY --from=build /app/crawler .
COPY --from=build /app/config.json .

RUN mkdir -p /app/data

ENTRYPOINT ["./crawler"]
