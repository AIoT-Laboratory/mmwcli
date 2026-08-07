override CGO_ENABLED := 0
export CGO_ENABLED

VERSION ?= dev
VERSION_LDFLAGS := -X=mmwcli/internal/app.Version=$(VERSION)

.PHONY: fmt fmt-check test vet build check

fmt:
	gofmt -w cmd internal

fmt-check:
	@files="$$(gofmt -l cmd internal)"; \
	if [ -n "$$files" ]; then \
		printf '%s\n' "$$files"; \
		exit 1; \
	fi

test:
	go test -count=1 ./...

vet:
	go vet ./...

build:
	go build -trimpath -ldflags "$(VERSION_LDFLAGS)" ./...

check: fmt-check test vet
