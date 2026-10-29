# Developer tasks. Each target runs the same command as CI.
# Windows contributors need GNU make (see README.md).

VERSION ?= $(shell git describe --tags --always --dirty)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test lint vet

all: build lint vet test

## build: compile every package and write the anthill binary to bin/
build:
	go build ./...
	go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/anthill

## test: run all tests with the race detector
test:
	go test -race ./...

## lint: run golangci-lint with .golangci.yml
lint:
	golangci-lint run

## vet: run go vet
vet:
	go vet ./...
