#!/bin/sh
# 安装 patato CLI（Linux / macOS），并建短别名 pat。
#
#   curl -fsSL https://github.com/momaek/patato/releases/latest/download/install-cli.sh | sh
#   curl -fsSL .../install-cli.sh | sh -s -- --version v0.2.0 --dir ~/.local/bin
#
# 下载与系统和架构对应的 patato-{os}-{arch}，用同一 Release 的 checksums.txt 校验后安装。
# 默认装到 /usr/local/bin（不可写时改用 ~/.local/bin）。Windows 请直接从 Release 页面下载 patato-windows-*.exe。
set -eu

REPO="${PATATO_REPO:-momaek/patato}"
VERSION=""
DIR=""

die() {
	echo "错误：$*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version) [ $# -ge 2 ] || die "--version 需要一个值"; VERSION="$2"; shift 2 ;;
	--dir) [ $# -ge 2 ] || die "--dir 需要一个值"; DIR="$2"; shift 2 ;;
	-h | --help)
		echo "用法：install-cli.sh [--version v0.1.0] [--dir 安装目录]"
		exit 0
		;;
	*) die "未知选项 $1" ;;
	esac
done

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "不支持的系统 $(uname -s)；Windows 请从 https://github.com/${REPO}/releases 下载" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "不支持的架构 $(uname -m)" ;;
esac

if [ -n "$VERSION" ]; then
	base="https://github.com/${REPO}/releases/download/${VERSION}"
else
	base="https://github.com/${REPO}/releases/latest/download"
fi

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		die "需要 curl 或 wget"
	fi
}

if [ -z "$DIR" ]; then
	DIR=/usr/local/bin
	if [ ! -w "$DIR" ] && [ "$(id -u)" != 0 ]; then
		DIR="$HOME/.local/bin"
	fi
fi
mkdir -p "$DIR"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
name="patato-${os}-${arch}"
echo "下载 ${name}（${VERSION:-latest}）"
fetch "${base}/${name}" "$tmp/$name" || die "下载失败：${base}/${name}"
fetch "${base}/checksums.txt" "$tmp/checksums.txt" || die "下载 checksums.txt 失败"

expected="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$tmp/checksums.txt")"
[ -n "$expected" ] || die "checksums.txt 里没有 ${name}"
if command -v sha256sum >/dev/null 2>&1; then
	actual="$(sha256sum "$tmp/$name" | awk '{ print $1 }')"
else
	actual="$(shasum -a 256 "$tmp/$name" | awk '{ print $1 }')"
fi
[ "$expected" = "$actual" ] || die "校验失败：期望 ${expected}，实际 ${actual}"

chmod 755 "$tmp/$name"
mv "$tmp/$name" "$DIR/patato"
ln -sf patato "$DIR/pat"
echo "已安装 $("$DIR/patato" version) 到 $DIR/patato（别名 pat）"
case ":$PATH:" in
*":$DIR:"*) ;;
*) echo "提示：$DIR 不在 PATH 里，把它加进 shell 配置，例如 export PATH=\"$DIR:\$PATH\"" ;;
esac
echo "登录：patato auth login --url https://api.你的域名"
