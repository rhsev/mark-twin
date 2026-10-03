BIN     := twin
BINDIR  ?= $(HOME)/bin
PREFIX  ?= /usr/local
# Build stamp shown by `twin --version`, so a running build is identifiable.
STAMP   := $(shell date +%Y-%m-%dT%H:%M)
LDFLAGS := -X github.com/rhsev/mark-twin/internal/twin.Build=$(STAMP)

.PHONY: build release link unlink install uninstall test clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/twin

release:
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w $(LDFLAGS)" -o twin-macos-arm64 ./cmd/twin
	GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w $(LDFLAGS)" -o twin-macos-amd64 ./cmd/twin
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w $(LDFLAGS)" -o twin-linux-amd64 ./cmd/twin
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w $(LDFLAGS)" -o twin-linux-arm64 ./cmd/twin

# Link once, then never again: rebuilding is deploying. Convention: ../BUILD.md.
link: build
	@install -d $(BINDIR)
	@ln -sfn $(CURDIR)/$(BIN) $(BINDIR)/$(BIN)
	@echo "linked $(BINDIR)/$(BIN) -> $(CURDIR)/$(BIN)"

unlink:
	rm -f $(BINDIR)/$(BIN)

# Published repo: `install` copies into $(PREFIX)/bin for anyone who clones
# this. A clone is not a stable place to point a symlink at.
install: build
	install -d $(PREFIX)/bin
	install -m 755 $(BIN) $(PREFIX)/bin/$(BIN)

uninstall:
	rm -f $(PREFIX)/bin/$(BIN)

test:
	go test ./...

clean:
	rm -f $(BIN) twin-macos-arm64 twin-macos-amd64 twin-linux-amd64 twin-linux-arm64
