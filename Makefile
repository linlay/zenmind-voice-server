APP_NAME := voice-server
BIN_DIR := bin
BIN_PATH := $(BIN_DIR)/$(APP_NAME)
VERSION ?= $(shell cat VERSION 2>/dev/null)
ARCH ?=

.PHONY: build run test frontend-install frontend-dev frontend-build docker-build docker-up docker-up-backend docker-down release clean

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

docker-up-backend:
	docker compose up --build -d voice-server-backend

docker-down:
	docker compose down

release:
	VERSION=$(VERSION) ARCH=$(ARCH) bash scripts/release.sh

clean:
	rm -f $(BIN_PATH)
