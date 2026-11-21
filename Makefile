# neko-box build targets.

NODE_IMAGE ?= neko-box/node:v1.33.1

.PHONY: node-image
node-image: ## Build the neko-owned node image (linux/arm64; requires `neko up`).
	docker buildx build --platform linux/arm64 --load \
		-t $(NODE_IMAGE) \
		-f images/node/Dockerfile .

.PHONY: build
build: ## Build the host neko binary.
	go build -o neko ./cmd/neko

.PHONY: test
test: ## Run all tests.
	go test ./...
