APP_NAME := voice-server
BIN_DIR := bin
BIN_PATH := $(BIN_DIR)/$(APP_NAME)

.PHONY: build run test frontend-install frontend-dev frontend-build docker-build docker-up docker-down clean

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_PATH) ./cmd/voice-server

run:
	go run ./cmd/voice-server

test:
	go test ./...

frontend-install:
	cd frontend && npm ci

frontend-dev: frontend-install
	cd frontend && npm run dev

frontend-build:
	cd frontend && npm run build

docker-build:
	docker compose build

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

clean:
	rm -f $(BIN_PATH)
