BINARY    := entire-agent-zed
INSTALL   := $(HOME)/.local/bin/$(BINARY)

.PHONY: build test install uninstall clean

build:
	go build -o $(BINARY) .

test:
	go test -v -count=1 ./...

install: build
	@mkdir -p $(dir $(INSTALL))
	cp $(BINARY) $(INSTALL)
	chmod 755 $(INSTALL)
	@echo "Installed to $(INSTALL)"

uninstall:
	rm -f $(INSTALL)
	@echo "Removed $(INSTALL)"

clean:
	rm -f $(BINARY)
