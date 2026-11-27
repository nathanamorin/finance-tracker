.PHONY: dev web web-dev test build docker run-docker keygen

DEV_DATA := $(CURDIR)/.data
# Local-only key, generated once and kept in .data (gitignored). Never use in a deployment.
DEV_KEY_FILE := $(DEV_DATA)/dev-master-key

$(DEV_KEY_FILE):
	@mkdir -p $(DEV_DATA)
	@openssl rand -base64 32 > $(DEV_KEY_FILE)
	@chmod 600 $(DEV_KEY_FILE)

# Run the API locally as user "dev" (no SSO). Pair with `make web-dev` for hot reload.
dev: $(DEV_KEY_FILE)
	FIN_TRACKER_MASTER_KEY=$$(cat $(DEV_KEY_FILE)) \
	FIN_TRACKER_DATA_DIR=$(DEV_DATA) \
	FIN_TRACKER_DEV_USER=dev \
	go run ./cmd/server

# Vite dev server on :5173, proxying /api to `make dev`.
web-dev:
	cd web && npm install && npm run dev

# Build the frontend into web/dist (embedded into the Go binary).
web:
	cd web && npm ci && npm run build && touch dist/.keep

test:
	go vet ./...
	go test ./...
	cd web && npm run typecheck && npm test

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o finance-tracker ./cmd/server

docker:
	docker build -t finance-tracker .

run-docker: $(DEV_KEY_FILE) docker
	docker run --rm -p 8080:8080 -v $(DEV_DATA):/data \
		-e FIN_TRACKER_MASTER_KEY=$$(cat $(DEV_KEY_FILE)) -e FIN_TRACKER_DEV_USER=dev finance-tracker

# Print a new master key (for the Kubernetes Secret).
keygen:
	@openssl rand -base64 32
