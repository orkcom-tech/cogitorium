BINARY := bin/cogitorium

.PHONY: build ui go desktop run test clean

# Sequential even under make -j: go embeds web/dist, so ui must finish first.
build:
	$(MAKE) ui
	$(MAKE) go

ui:
	cd web && npm ci --no-audit --no-fund && npm run build
	@touch web/dist/.gitkeep # vite empties dist; keep the dir in git so bare `go build` works

go:
	go build -o $(BINARY) ./cmd/cogitorium

# The desktop shell. Needs cgo and the platform's webview headers — on Linux
# that is libgtk-3-dev and libwebkit2gtk-4.1-dev — which is exactly why it is
# a separate target and a separate binary: `make build` stays pure Go and
# cross-compilable, and only this one is tied to the machine it is built on.
desktop: ui
	CGO_ENABLED=1 go build -o bin/cogitorium-desktop ./cmd/cogitorium-desktop

run: build
	./$(BINARY) serve

test:
	go test ./...

clean:
	rm -rf bin web/dist web/node_modules
	@mkdir -p web/dist && touch web/dist/.gitkeep # keep the embed dir so bare `go build` works
