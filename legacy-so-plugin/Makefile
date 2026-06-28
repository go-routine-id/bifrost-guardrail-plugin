# Bifrost loads Go plugins via plugin.Open, which requires the .so to be built
# with the SAME Go version (1.26.4), GOOS/GOARCH (linux/amd64), and core module
# version (v1.6.0) as the running bifrost binary. Build inside the matching image.

PLUGIN  := guardrail.so
GO_IMG  := golang:1.26.4
CORE    := v1.6.0

.PHONY: build build-docker tidy clean

# Native build (use only on a linux/amd64 host with Go 1.26.4).
build:
	CGO_ENABLED=1 go build -buildmode=plugin -trimpath -o $(PLUGIN) .

# Reproducible build matching the Bifrost runtime. Produces ./$(PLUGIN).
build-docker:
	docker run --rm --platform linux/amd64 \
		-v "$(CURDIR)":/src -w /src \
		-e CGO_ENABLED=1 -e GOFLAGS=-mod=mod \
		$(GO_IMG) \
		sh -c "go get github.com/maximhq/bifrost/core@$(CORE) && go mod tidy && go build -buildmode=plugin -trimpath -o $(PLUGIN) ."

tidy:
	go mod tidy

clean:
	rm -f $(PLUGIN)
