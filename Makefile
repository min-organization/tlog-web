GOOS ?= linux
GOARCH ?= amd64

.PHONY: build
build:
	./scripts/build.sh $(GOOS) $(GOARCH)

.PHONY: frontend
frontend:
	cd frontend && npm ci --legacy-peer-deps && npm run build

.PHONY: run
run: frontend
	cd backend && go run .

.PHONY: image
image:
	docker compose build

.PHONY: clean
clean:
	rm -f tlog-web-* backend/frontend/dist frontend/dist
