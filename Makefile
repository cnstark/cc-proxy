IMAGE ?= cc-proxy:latest
PLATFORMS ?= linux/amd64,linux/arm64

VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build
build:
	go build -ldflags="$(LDFLAGS)" -o bin/ccp ./cmd/ccp
	go build -ldflags="$(LDFLAGS)" -o bin/ccp-proxy ./cmd/ccp-proxy

.PHONY: build-all
build-all:
	GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o bin/ccp-linux-amd64   ./cmd/ccp
	GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o bin/ccp-proxy-linux-amd64 ./cmd/ccp-proxy
	GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o bin/ccp-linux-arm64   ./cmd/ccp
	GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o bin/ccp-proxy-linux-arm64 ./cmd/ccp-proxy
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o bin/ccp-windows-amd64.exe   ./cmd/ccp
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o bin/ccp-proxy-windows-amd64.exe ./cmd/ccp-proxy
	GOOS=darwin  GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o bin/ccp-darwin-arm64  ./cmd/ccp
	GOOS=darwin  GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o bin/ccp-proxy-darwin-arm64 ./cmd/ccp-proxy

.PHONY: test
test:
	go test ./...

.PHONY: clean
clean:
	rm -rf bin/

.PHONY: docker-build-single
docker-build-single:
	docker build -t $(IMAGE) .

.PHONY: docker-build
docker-build:
	docker buildx build --platform $(PLATFORMS) -t $(IMAGE) --push .

.PHONY: docker-push
docker-push:
	docker buildx build --platform $(PLATFORMS) -t $(IMAGE) --push .
