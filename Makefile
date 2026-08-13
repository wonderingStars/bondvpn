BINARY  := bondvpn
VERSION := 1.6.0
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
# minimal ones these boxes usually are - and on a NAS, whose userland is often
# far older than the kernel.
#
#   amd64  ordinary x86 servers, and Synology's Intel/AMD models
#   arm64  Raspberry Pi 4/5, and Synology's newer ARM models
#   armv7  older 32-bit ARM: Pi 2/3 on a 32-bit OS, and Synology's DS-j models
release: vet test
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(FLAGS) -o dist/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(FLAGS) -o dist/$(BINARY)-linux-arm64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(FLAGS) -o dist/$(BINARY)-linux-armv7 .

clean:
	rm -rf dist $(BINARY)
