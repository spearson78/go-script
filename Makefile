.PHONY: all
all:
	GOOS=js GOARCH=wasm go build -o bin/playground.wasm ./cmd/playground
	go build ./cmd/server