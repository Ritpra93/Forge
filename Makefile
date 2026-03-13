.PHONY: build test lint proto docker clean

build:
	go build ./...

test:
	go test -race ./...

lint:
	golangci-lint run

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       internal/proto/forgepb/forge.proto

docker:
	docker-compose -f deploy/docker-compose.yml build

clean:
	rm -f bin/scheduler bin/worker bin/forgectl
	go clean ./...
