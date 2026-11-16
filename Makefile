BINARY := netprobe
PKG     := ./cmd/netprobe
VERSION ?= $(shell date +%Y%m%d)
DIST    := dist-$(VERSION)

.PHONY: build run test clean package

build:
	go build -trimpath -ldflags "-s -w -X netprobe/internal/web.Version=$(VERSION)" -o bin/$(BINARY) $(PKG)

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t netprobe:dev .

run: build
	./bin/$(BINARY) -config configs/config.example.yaml

test:
	go test ./...

# 打包发布产物（Linux 服务器部署）
package: build
	rm -rf $(DIST) && mkdir -p $(DIST)
	cp bin/$(BINARY) $(DIST)/
	cp configs/config.example.yaml $(DIST)/
	cp deploy/netprobe.service deploy/install.sh $(DIST)/
	cp README.md $(DIST)/ 2>/dev/null || true
	chmod +x $(DIST)/install.sh
	tar czf $(DIST).tar.gz $(DIST)
	rm -rf $(DIST)
	@echo "==> $(DIST).tar.gz"

clean:
	rm -rf bin dist-*
