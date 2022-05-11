RELEASE = v1.0
BUILD_COMMIT := $(shell git rev-parse --short HEAD)
DATE := $(shell date -u '+%F %X UTC')
VERSION := ${RELEASE}, rev ${BUILD_COMMIT}, compiled at ${DATE}
GOPATH := $(shell go env GOPATH)

.EXPORT_ALL_VARIABLES:
PATH := ${PATH}:${GOPATH}/bin

define build_daemon
	go build -o $(1)/nocc-daemon -trimpath -ldflags '-s -w -X "github.com/VKCOM/nocc/internal/common.version=${VERSION}"' cmd/nocc-daemon/main.go
endef

define build_server
	go build -o $(1)/nocc-server -trimpath -ldflags '-s -w -X "github.com/VKCOM/nocc/internal/common.version=${VERSION}"' cmd/nocc-server/main.go
endef

protogen:
	protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative pb/nocc-protobuf.proto

lint:
	golangci-lint run

client:
	$(call build_daemon,bin)
	g++ -std=c++11 -O3 cmd/nocc.cpp -o bin/nocc

server:
	$(call build_server,bin)

github_release:
# compile for Mac M1 (could be done only if running Mac)
ifeq ($(shell uname), Darwin)
	rm -f bin/nocc-${RELEASE}-darwin-arm64.tar.gz
	mkdir bin/darwin-arm64
	GOOS=darwin GOARCH=arm64 $(call build_daemon,bin/darwin-arm64)
	GOOS=darwin GOARCH=arm64 $(call build_server,bin/darwin-arm64)
	clang++ -arch arm64 -std=c++11 -O3 cmd/nocc.cpp -o bin/darwin-arm64/nocc
	cd bin/darwin-arm64 && tar -czf ../nocc-${RELEASE}-darwin-arm64.tar.gz .
	rm -rf bin/darwin-arm64
endif

# compile for Mac Intel (could be done only if running Mac)
ifeq ($(shell uname), Darwin)
	rm -f bin/nocc-${RELEASE}-darwin-amd64.tar.gz
	mkdir bin/darwin-amd64
	GOOS=darwin GOARCH=amd64 $(call build_daemon,bin/darwin-amd64)
	GOOS=darwin GOARCH=amd64 $(call build_server,bin/darwin-amd64)
	clang++ -arch x86_64 -std=c++11 -O3 cmd/nocc.cpp -o bin/darwin-amd64/nocc
	cd bin/darwin-amd64 && tar -czf ../nocc-${RELEASE}-darwin-amd64.tar.gz .
	rm -rf bin/darwin-amd64
endif

# compile for Linux (could be done only if running Linux)
ifeq ($(shell uname), Linux)
	rm -f bin/nocc-${RELEASE}-linux-amd64.tar.gz
	mkdir bin/linux-amd64
	$(call build_daemon,bin/linux-amd64)
	$(call build_server,bin/linux-amd64)
	g++ -std=c++11 -O3 cmd/nocc.cpp -o bin/linux-amd64/nocc
	cd bin/linux-amd64 && tar -czf ../nocc-${RELEASE}-linux-amd64.tar.gz .
	rm -rf bin/linux-amd64
endif


.DEFAULT_GOAL := all
all: protogen lint client server
.PHONY : all

clean:
	rm -f bin/nocc bin/nocc-daemon bin/nocc-server
