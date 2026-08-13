BINARY  := bondvpn
VERSION := 1.5.2
FLAGS   := -trimpath -ldflags "-s -w"

.PHONY: all test vet release clean

all: vet test
	go build $(FLAGS) -o $(BINARY) .

test:
	go test -count=1 ./...

vet:
	gofmt -l .
	go vet ./...

# CGO off so the binaries are static and run on any distribution, including the
# minimal ones these boxes usually are.
release: vet test
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(FLAGS) -o dist/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(FLAGS) -o dist/$(BINARY)-linux-arm64 .

clean:
	rm -rf dist $(BINARY)
