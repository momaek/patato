SQLC := docker run --rm -v $(CURDIR):/src -w /src sqlc/sqlc:1.27.0

.PHONY: build test generate lint check-generated

build:
	go build -o bin/patato-server ./server/cmd/patato-server

# 集成测试需要 PostgreSQL：设置 PATATO_TEST_DATABASE_URL，或本机有 Docker（自动起 testcontainers）
test:
	go test ./...

# 改了 server/internal/store/queries 或 migrations 后重新生成查询代码
generate:
	$(SQLC) generate

lint:
	go vet ./...
	test -z "$$(gofmt -l server api)"

check-generated: generate
	git diff --exit-code server/internal/store/db
