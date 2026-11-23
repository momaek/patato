#!/usr/bin/env bash
# 准备 GitHub Release 的全部附件到 dist/：
#   - CLI：Linux / macOS / Windows × amd64 / arm64
#   - patato-node：Linux × amd64 / arm64
#   - 一键部署文件：install.sh、install-cli.sh、docker-compose.yaml、Caddyfile、config.example.yaml
#   - checksums.txt（sha256sum 格式，覆盖以上全部文件）
# 服务端以 Docker 镜像发布（ghcr.io/momaek/patato-server），不在这里构建。
#
# 用法：scripts/release.sh v0.1.0
#
# 文件名是固定约定，不要随意改：
#   patato-node-linux-{arch}     服务端 node.binary_url 默认值和面板生成的安装命令依赖它
#   patato-{os}-{arch}[.exe]     scripts/install-cli.sh 依赖它
#   部署文件名                    deploy/install.sh 按版本从 Release 下载它们
set -euo pipefail

version="${1:-}"
if [[ -z "$version" ]]; then
	echo "用法：$0 <版本号，例如 v0.1.0>" >&2
	exit 2
fi

cd "$(dirname "$0")/.."
out="${DIST_DIR:-dist}"
rm -rf "$out"
mkdir -p "$out"

ldflags="-s -w -X main.version=${version}"
# patato-node 内嵌 sing-box，需要这些构建标签才包含 QUIC（Hysteria2 / TUIC）、Reality 和 ACME；与 Makefile 保持一致
node_tags="with_quic,with_utls,with_acme"

build() {
	local goos="$1" goarch="$2" pkg="$3" name="$4" tags="${5:-}"
	echo "build ${name}"
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
		go build -trimpath -tags "$tags" -ldflags "$ldflags" -o "${out}/${name}" "$pkg"
}

for arch in amd64 arm64; do
	build linux "$arch" ./node/cmd/patato-node "patato-node-linux-${arch}" "$node_tags"
done

for os in linux darwin windows; do
	for arch in amd64 arm64; do
		ext=""
		[[ "$os" == windows ]] && ext=".exe"
		build "$os" "$arch" ./cli/cmd/patato "patato-${os}-${arch}${ext}"
	done
done

cp deploy/install.sh scripts/install-cli.sh deploy/docker-compose.yaml deploy/Caddyfile deploy/config.example.yaml "$out/"

(
	cd "$out"
	files=(patato-* install.sh install-cli.sh docker-compose.yaml Caddyfile config.example.yaml)
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "${files[@]}" >checksums.txt
	else
		shasum -a 256 "${files[@]}" >checksums.txt
	fi
)
echo "完成：${out}/"
ls -l "$out"
