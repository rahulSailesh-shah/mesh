.PHONY: build run clean

PORT ?= 9876
NODE_ID ?=

build:
	go build -o mesh ./cmd/mesh

run: build
	./mesh -port $(PORT) $(if $(NODE_ID),-node-id $(NODE_ID))

clean:
	rm -f mesh
