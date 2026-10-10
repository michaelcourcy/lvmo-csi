GO ?= go
VERSION ?= dev
IMAGE ?= michaelcourcy/lvmo-csi
.PHONY: build test generate lint release-binaries image e2e
build:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags='-X main.version=$(VERSION)' -o bin/lvmo-csi ./cmd/lvmo-csi
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags='-X main.version=$(VERSION)' -o bin/lvmo-driver ./cmd/lvmo-driver
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o bin/lvmo-metadata ./cmd/lvmo-metadata
test:
	$(GO) test -race ./...
lint:
	$(GO) vet ./...
	bash scripts/test-pod-storage-chart.sh
	bash scripts/test-release-chart.sh
generate:
	protoc --go_out=. --go_opt=module=github.com/michaelcourcy/lvmo-csi --go-grpc_out=. --go-grpc_opt=module=github.com/michaelcourcy/lvmo-csi api/v1/storage.proto
release-binaries:
	VERSION=$(VERSION) bash scripts/build-release.sh
image:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) --push .
e2e:
	bash scripts/e2e.sh
