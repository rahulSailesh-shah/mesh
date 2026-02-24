.PHONY: build run clean

PORT ?=
NODE_ID ?=

build:
	go build -o mesh ./cmd/mesh

run: build
ifndef PORT
	$(error PORT is required. Usage: make run PORT=9876 NODE_ID=node1)
endif
ifndef NODE_ID
	$(error NODE_ID is required. Usage: make run PORT=9876 NODE_ID=node1)
endif
	./mesh -port $(PORT) -node-id $(NODE_ID)

clean:
	rm -f mesh
