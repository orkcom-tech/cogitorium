BINARY := bin/cogitorium

.PHONY: build ui go run test clean

build: ui go

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
