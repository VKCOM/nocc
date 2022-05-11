#!/bin/sh

sudo apt install protobuf-compiler

cd /tmp

export GO111MODULE=on  # Enable module mode
go get -u google.golang.org/protobuf/cmd/protoc-gen-go 
go get -u google.golang.org/grpc/cmd/protoc-gen-go-grpc
go get github.com/golangci/golangci-lint/cmd/golangci-lint
