PLUGIN_NAME ?= codex-switch-guard
VERSION ?= 1.0.0
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GO_LDFLAGS ?= -s -w -X main.pluginVersion=$(VERSION)

EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
PLUGIN_EXT = $(or $(EXT_$(GOOS)),so)
PLUGIN_OUTPUT ?= dist/$(PLUGIN_NAME).$(PLUGIN_EXT)

.PHONY: test vet build clean package

test:
	go test ./...

vet:
	go vet ./...

build:
	mkdir -p dist
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -buildmode=c-shared -ldflags "$(GO_LDFLAGS)" -o $(PLUGIN_OUTPUT) .
	rm -f dist/$(PLUGIN_NAME).h

package: build
	@version=$(VERSION); archive=$(PLUGIN_NAME)_$${version}_$(GOOS)_$(GOARCH).zip; \
	go run ./.github/scripts/package-release.go \
		-library "$(PLUGIN_OUTPUT)" \
		-archive "$${archive}" \
		-checksum "$${archive}.sha256" \
		-manifest "$${archive}.artifact.json" \
		-version "$${version}" \
		-goos "$(GOOS)" \
		-goarch "$(GOARCH)" \
		-source-commit "$$(git rev-parse HEAD)"

clean:
	rm -rf dist
	rm -f $(PLUGIN_NAME)_*.zip $(PLUGIN_NAME)_*.sha256 $(PLUGIN_NAME)_*.artifact.json checksums.txt artifact-manifest.json
