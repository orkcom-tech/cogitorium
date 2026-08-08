BINARY := bin/cogitorium

.PHONY: build ui go run test clean

# Sequential even under make -j: go embeds web/dist, so ui must finish first.
build:
	$(MAKE) ui
	$(MAKE) go

ui:
	cd web && npm ci --no-audit --no-fund && npm run build
	@touch web/dist/.gitkeep # vite empties dist; keep the dir in git so bare `go build` works

go:
	go build -o $(BINARY) ./cmd/cogitorium

run: build
	./$(BINARY) serve

test:
	go test ./...

clean:
	rm -rf bin web/dist web/node_modules
	@mkdir -p web/dist && touch web/dist/.gitkeep # keep the embed dir so bare `go build` works
