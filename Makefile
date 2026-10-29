.PHONY: build test test-short vet fmt cross notices clean
build:
	go build -o gg ./cmd/gg
test:
	go test ./...
test-short:
	go test -short ./...
vet:
	go vet ./...
fmt:
	gofmt -l -w .
cross:
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
	  GOOS=$${t%/*} GOARCH=$${t#*/} go build -o /dev/null ./cmd/gg && echo "ok $$t"; done
notices:
	go run ./scripts/notices > THIRD_PARTY_NOTICES.md
clean:
	rm -rf gg gg.exe dist
