# Frontend build stage: compile the React dashboard into web/dist.
FROM node:24-alpine AS web

WORKDIR /web

# Cache dependencies
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

# Go build stage: embed the built dashboard and compile a static binary.
FROM golang:1.26-alpine AS build

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Overlay the freshly built dashboard so //go:embed all:dist picks it up.
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o crawler ./cmd/server

# Runtime stage
FROM alpine:latest

WORKDIR /app

COPY --from=build /app/crawler .

EXPOSE 8080

ENTRYPOINT ["./crawler"]
