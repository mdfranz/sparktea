BINARY := sparktea
PREFIX := $(HOME)/.local/bin

.PHONY: build install clean

build:
	go build -o $(BINARY) ./cmd/sparktea

install: build
	mkdir -p $(PREFIX)
	install -m 755 $(BINARY) $(PREFIX)/$(BINARY)

clean:
	rm -f $(BINARY)
