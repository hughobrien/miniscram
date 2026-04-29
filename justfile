default: check

build:
    go build ./...

test:
    go test -short -count=1 ./...

test-full:
    go test -count=1 ./...

vet:
    go vet ./...

fmt:
    gofmt -w .

fmt-check:
    @out=$(gofmt -l .); if [ -n "$out" ]; then echo "$out"; exit 1; fi

check: vet fmt-check test

# Sanitizers (require CGO + a C compiler; CC defaults to clang).
test-race:
    CC=clang CGO_ENABLED=1 go test -race -short -count=1 ./...

test-msan:
    CC=clang CGO_ENABLED=1 go test -msan -short -count=1 ./...

test-asan:
    CC=clang CGO_ENABLED=1 go test -asan -short -count=1 ./...

test-sanitizers: test-race test-msan test-asan

clean:
    rm -f miniscram
    go clean -testcache
