SQLC := docker run --rm -v $(CURDIR):/src -w /src sqlc/sqlc:1.27.0

.PHONY: build web install-cli test generate lint check-generated

# patato-node 内嵌 sing-box，需要这些构建标签才包含 QUIC（Hysteria2 / TUIC）、Reality 和 ACME
NODE_TAGS := with_quic,with_utls,with_acme

build:
	go build -o bin/patato-server ./server/cmd/patato-server
	go build -tags $(NODE_TAGS) -o bin/patato-node ./node/cmd/patato-node
	go build -o bin/patato ./cli/cmd/patato

# 安装 CLI 到 $(GOBIN)（默认 ~/go/bin），并建短别名 pat
install-cli:
	go install ./cli/cmd/patato
	ln -sf patato "$$(go env GOPATH)/bin/pat"

# 构建两个前端，产物放到 deploy/web 下由 Caddy 托管
web:
	cd web && pnpm install --frozen-lockfile && pnpm build
	rm -rf deploy/web && mkdir -p deploy/web
	cp -r web/admin/dist deploy/web/admin
	cp -r web/portal/dist deploy/web/portal

# 集成测试需要 PostgreSQL：设置 PATATO_TEST_DATABASE_URL，或本机有 Docker（自动起 testcontainers）
test:
	go test -tags $(NODE_TAGS) ./...

# 改了 server/internal/store/queries 或 migrations 后重新生成查询代码
generate:
	$(SQLC) generate

lint:
	go vet -tags $(NODE_TAGS) ./...
	test -z "$$(gofmt -l server api node nodeapi cli)"

check-generated: generate
	git diff --exit-code server/internal/store/db
