.PHONY: build web-build server-build dev test test-race docker-up clean

# build produces the single self-contained binary: the dashboard is built first
# so `go build` can embed web/dist via web/web.go's //go:embed directive.
build: web-build server-build

# web-build compiles the React dashboard into web/dist (embedded by the server).
web-build:
	cd web && npm ci && npm run build

# server-build compiles the server binary (embeds the current web/dist).
server-build:
	go build -o bin/crawler ./cmd/server

# dev runs the Vite dev server, which proxies /api to a locally running server
# (start `go run ./cmd/server` separately, with Postgres + Redis up).
dev:
	cd web && npm run dev

test:
	go test ./...

test-race:
	go test -race ./...

# docker-up builds the image and starts the full stack (Postgres, Redis,
# crawler, and the observability services).
docker-up:
	docker compose up --build

clean:
	rm -rf bin web/node_modules web/dist/assets
