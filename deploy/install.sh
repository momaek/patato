#!/usr/bin/env bash
# Patato 一键部署 / 升级（Docker Compose）。
#
# 首次安装，只用一个域名（先解析到这台机器）：
#   curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash -s -- --host panel.example.com
#   API、订阅、用户侧都在 https://panel.example.com，Admin 在随机路径 https://panel.example.com/<随机串>
#
# 或者用三个子域名：
#   ... | sudo bash -s -- --domain example.com
#   等价于 --api-host api.example.com --admin-host admin.example.com --portal-host my.example.com
#
# 升级到最新版本（或 --version v0.2.0 指定版本）：在同一台机器上再执行一次，不用带域名
#   curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash
#
# 做的事：装 Docker（缺少时）→ 下载对应版本的 Compose 文件到 /opt/patato → 首次生成 .env 和 config.yaml
# （随机数据库密码和 secret_key）→ 升级前备份数据库 → 拉镜像并启动 → 等服务端健康 → 首次安装打印运营者邀请链接。
# 已有的 config.yaml、.env、数据和 docker-compose.override.yaml 不会被覆盖。
set -euo pipefail

REPO="${PATATO_REPO:-momaek/patato}"
DIR="/opt/patato"
VERSION=""
DOMAIN=""
HOST=""
ADMIN_PATH=""
API_HOST=""
ADMIN_HOST=""
PORTAL_HOST=""
FROM_DIR=""
IMAGE=""
HTTP_PORT=""
HTTPS_PORT=""
OPERATOR="admin"
INSTALL_DOCKER=1
START=1
PULL=1

usage() {
	cat <<'EOF'
用法：install.sh [选项]

  --host <域名>            只用一个域名：API、订阅、用户侧都在它下面，Admin 挂在随机路径下
  --admin-path <路径>      配合 --host 指定 Admin 路径（默认随机生成，例如 a8f3k2c9e1b0）
  --domain <域名>          用 api.<域名>、admin.<域名>、my.<域名> 三个子域名
  --api-host <主机名>      API、订阅和节点接入的域名（默认 api.<域名>）
  --admin-host <主机名>    Admin 的域名（默认 admin.<域名>）
  --portal-host <主机名>   用户侧的域名（默认 my.<域名>）
  --version <版本>         安装或升级到的版本，例如 v0.2.0（默认最新 Release）
  --dir <目录>             安装目录（默认 /opt/patato）
  --operator <名字>        首次安装时邀请的第一个运营者（默认 admin）
  --http-port <端口>       Caddy 对外的 HTTP 端口（默认 80）
  --https-port <端口>      Caddy 对外的 HTTPS 端口（默认 443）
  --no-install-docker      缺少 Docker 时报错退出，不自动安装
  --no-start               只准备文件，不拉镜像、不启动
  --from-dir <目录>        从本地目录取 Compose 文件，不从 GitHub 下载（离线或测试用）
  --image <镜像名>         服务端镜像名（默认 ghcr.io/momaek/patato-server）
  --no-pull                不拉镜像，直接用本机已有的（配合本地构建的镜像）
  -h, --help               显示帮助

升级时域名、端口等沿用安装目录 .env 里的值，只需要再执行一次。
EOF
}

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m警告：\033[0m%s\n' "$*" >&2; }
die() {
	printf '\033[1;31m错误：\033[0m%s\n' "$*" >&2
	exit 1
}

need_arg() { [[ $# -ge 2 && -n "$2" ]] || die "$1 需要一个值"; }

while [[ $# -gt 0 ]]; do
	case "$1" in
	--host) need_arg "$@"; HOST="$2"; shift 2 ;;
	--admin-path) need_arg "$@"; ADMIN_PATH="$2"; shift 2 ;;
	--domain) need_arg "$@"; DOMAIN="$2"; shift 2 ;;
	--api-host) need_arg "$@"; API_HOST="$2"; shift 2 ;;
	--admin-host) need_arg "$@"; ADMIN_HOST="$2"; shift 2 ;;
	--portal-host) need_arg "$@"; PORTAL_HOST="$2"; shift 2 ;;
	--version) need_arg "$@"; VERSION="$2"; shift 2 ;;
	--dir) need_arg "$@"; DIR="$2"; shift 2 ;;
	--operator) need_arg "$@"; OPERATOR="$2"; shift 2 ;;
	--http-port) need_arg "$@"; HTTP_PORT="$2"; shift 2 ;;
	--https-port) need_arg "$@"; HTTPS_PORT="$2"; shift 2 ;;
	--from-dir) need_arg "$@"; FROM_DIR="$2"; shift 2 ;;
	--image) need_arg "$@"; IMAGE="$2"; shift 2 ;;
	--no-install-docker) INSTALL_DOCKER=0; shift ;;
	--no-start) START=0; shift ;;
	--no-pull) PULL=0; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "未知选项 $1" ;;
	esac
done

# ---------- 工具函数 ----------

valid_host() { [[ "$1" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]]; }
valid_port() { [[ "$1" =~ ^[0-9]+$ ]] && ((10#$1 >= 1 && 10#$1 <= 65535)); }

rand_hex() {
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -hex "$1"
	else
		od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'
	fi
}

# env_get <文件> <键>：读 KEY=VALUE 形式的值
env_get() {
	[[ -f "$1" ]] || return 0
	sed -n "s/^$2=//p" "$1" | tail -n1
}

# env_set <文件> <键> <值>：有则替换，无则追加
env_set() {
	local file="$1" key="$2" value="$3" tmp
	tmp="$(mktemp)"
	if [[ -f "$file" ]] && grep -q "^${key}=" "$file"; then
		awk -v k="$key" -v v="$value" 'index($0, k "=") == 1 { print k "=" v; next } { print }' "$file" >"$tmp"
	else
		[[ -f "$file" ]] && cat "$file" >"$tmp"
		printf '%s=%s\n' "$key" "$value" >>"$tmp"
	fi
	cat "$tmp" >"$file"
	rm -f "$tmp"
}

# config_host <config.yaml> <键>：从 public_url 这类顶层配置里取出主机名
config_host() {
	[[ -f "$1" ]] || return 0
	sed -n "s/^$2:[[:space:]]*[\"']\{0,1\}https\{0,1\}:\/\/\([^:/\"']*\).*/\1/p" "$1" | head -n1
}

# config_path <config.yaml> <键>：取出 admin_url 这类配置里的路径（不含开头的 /）
config_path() {
	[[ -f "$1" ]] || return 0
	sed -n "s/^$2:[[:space:]]*[\"']\{0,1\}https\{0,1\}:\/\/[^/\"']*\/\([^/\"']*\).*/\1/p" "$1" | head -n1
}

fetch() { # fetch <URL> <目标文件>
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		die "需要 curl 或 wget"
	fi
}

latest_version() {
	local url
	if command -v curl >/dev/null 2>&1; then
		url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")" || return 1
	else
		url="$(wget -q --max-redirect=5 -S -O /dev/null "https://github.com/${REPO}/releases/latest" 2>&1 | sed -n 's/^ *Location: *//p' | tail -n1 | tr -d '\r')"
	fi
	url="${url%/}"
	[[ "$url" == */releases/tag/* ]] || return 1
	printf '%s\n' "${url##*/}"
}

# 把文件装到安装目录；已存在且内容不同时，旧文件留成 .bak
install_file() { # install_file <来源> <目标>
	if [[ -f "$2" ]] && ! cmp -s "$1" "$2"; then
		cp "$2" "$2.bak"
		log "已更新 $(basename "$2")，旧文件备份为 $(basename "$2").bak"
	fi
	cp "$1" "$2"
}

# 经 curl | bash 执行时脚本本身来自标准输入，子进程不能读它，否则会吃掉脚本剩下的部分
compose() { (cd "$DIR" && docker compose "$@" </dev/null); }

# ---------- 检查 Docker ----------

ensure_docker() {
	if ! command -v docker >/dev/null 2>&1; then
		((INSTALL_DOCKER)) || die "没有找到 docker；去掉 --no-install-docker 可以自动安装"
		[[ "$(uname -s)" == Linux ]] || die "没有找到 docker，请先安装 Docker Desktop"
		[[ $EUID -eq 0 ]] || die "自动安装 Docker 需要 root，请用 sudo 执行"
		log "安装 Docker（get.docker.com）"
		local script
		script="$(mktemp)"
		fetch https://get.docker.com "$script"
		sh "$script" </dev/null
		rm -f "$script"
		systemctl enable --now docker >/dev/null 2>&1 || true
	fi
	docker info >/dev/null 2>&1 || die "无法连接 Docker；请确认 Docker 已启动，并用 root 或 docker 组用户执行"
	docker compose version >/dev/null 2>&1 || die "需要 Docker Compose v2 插件（docker compose）"
}

# ---------- 准备文件 ----------

mkdir -p "$DIR"
DIR="$(cd "$DIR" && pwd)"
ENV_FILE="$DIR/.env"
CONFIG="$DIR/config.yaml"

FIRST_INSTALL=1
[[ -f "$CONFIG" ]] && FIRST_INSTALL=0

# 版本：参数 > 最新 Release；--from-dir 时可以不指定（镜像用 .env 里的或 latest）
if [[ -z "$VERSION" && -z "$FROM_DIR" ]]; then
	VERSION="$(latest_version)" || die "查询最新版本失败；可以用 --version 指定，例如 --version v0.1.0"
fi
[[ -z "$VERSION" ]] && VERSION="$(env_get "$ENV_FILE" PATATO_VERSION)"
[[ -z "$VERSION" ]] && VERSION="latest"

# 域名：参数 > .env > 已有 config.yaml
if [[ -n "$HOST" ]]; then
	[[ -z "$DOMAIN$API_HOST$ADMIN_HOST$PORTAL_HOST" ]] || die "--host 不能和 --domain、--api-host、--admin-host、--portal-host 一起用"
	valid_host "$HOST" || die "--host 不是合法的域名：$HOST"
	API_HOST="$HOST" ADMIN_HOST="$HOST" PORTAL_HOST="$HOST"
elif [[ -n "$ADMIN_PATH" ]]; then
	die "--admin-path 只能配合 --host 使用"
fi
if [[ -n "$DOMAIN" ]]; then
	valid_host "$DOMAIN" || die "--domain 不是合法的域名：$DOMAIN"
	API_HOST="${API_HOST:-api.$DOMAIN}"
	ADMIN_HOST="${ADMIN_HOST:-admin.$DOMAIN}"
	PORTAL_HOST="${PORTAL_HOST:-my.$DOMAIN}"
fi
API_HOST="${API_HOST:-$(env_get "$ENV_FILE" PATATO_API_HOST)}"
ADMIN_HOST="${ADMIN_HOST:-$(env_get "$ENV_FILE" PATATO_ADMIN_HOST)}"
PORTAL_HOST="${PORTAL_HOST:-$(env_get "$ENV_FILE" PATATO_PORTAL_HOST)}"
API_HOST="${API_HOST:-$(config_host "$CONFIG" public_url)}"
ADMIN_HOST="${ADMIN_HOST:-$(config_host "$CONFIG" admin_url)}"
PORTAL_HOST="${PORTAL_HOST:-$(config_host "$CONFIG" portal_url)}"
if [[ -z "$API_HOST" || -z "$ADMIN_HOST" || -z "$PORTAL_HOST" ]]; then
	usage >&2
	die "首次安装需要域名：--domain example.com，或分别指定 --api-host、--admin-host、--portal-host"
fi
for h in "$API_HOST" "$ADMIN_HOST" "$PORTAL_HOST"; do
	valid_host "$h" || die "不是合法的主机名：${h}（只写域名，不带 https:// 和路径）"
	[[ "$h" == example.com || "$h" == *.example.com ]] && die "请把 $h 换成你自己的域名"
done
# Admin 和用户侧同一个域名时，Admin 挂在一段随机路径下（只在首次生成配置时用到；之后以 config.yaml 为准）
SINGLE_HOST=0
if [[ "$ADMIN_HOST" == "$PORTAL_HOST" ]]; then
	SINGLE_HOST=1
	ADMIN_PATH="${ADMIN_PATH#/}"
	ADMIN_PATH="${ADMIN_PATH%/}"
	ADMIN_PATH="${ADMIN_PATH:-$(config_path "$CONFIG" admin_url)}"
	if [[ -z "$ADMIN_PATH" ]]; then
		((FIRST_INSTALL)) || die "Admin 和用户侧是同一个域名，但 config.yaml 的 admin_url 没有路径；请在 admin_url 后面加一段随机路径"
		ADMIN_PATH="$(rand_hex 6)"
	fi
	[[ "$ADMIN_PATH" =~ ^[A-Za-z0-9_-]+$ ]] || die "--admin-path 只能包含字母、数字、- 和 _：$ADMIN_PATH"
	# 与服务端 config.reservedAdminPaths 一致：不能占用 API 和用户侧页面的路径
	case "$(printf '%s' "$ADMIN_PATH" | tr '[:upper:]' '[:lower:]')" in
	api | assets | healthz | metrics | login | forgot-password | reset-password | password | reset-link | register | buy | orders | tickets | invite)
		die "--admin-path 不能用 ${ADMIN_PATH}，它和 API 或用户侧页面冲突；换一个随机的" ;;
	esac
fi

HTTP_PORT="${HTTP_PORT:-$(env_get "$ENV_FILE" HTTP_PORT)}"
HTTPS_PORT="${HTTPS_PORT:-$(env_get "$ENV_FILE" HTTPS_PORT)}"
HTTP_PORT="${HTTP_PORT:-80}"
HTTPS_PORT="${HTTPS_PORT:-443}"
valid_port "$HTTP_PORT" || die "--http-port 不是合法端口：$HTTP_PORT"
valid_port "$HTTPS_PORT" || die "--https-port 不是合法端口：$HTTPS_PORT"
IMAGE="${IMAGE:-$(env_get "$ENV_FILE" PATATO_IMAGE)}"
IMAGE="${IMAGE:-ghcr.io/momaek/patato-server}"

((START)) && ensure_docker

# Compose 文件：同一版本的 docker-compose.yaml、Caddyfile、config.example.yaml
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
files=(docker-compose.yaml Caddyfile config.example.yaml)
if [[ -n "$FROM_DIR" ]]; then
	for f in "${files[@]}"; do
		[[ -f "$FROM_DIR/$f" ]] || die "$FROM_DIR 里没有 $f"
		cp "$FROM_DIR/$f" "$tmpdir/$f"
	done
else
	log "下载 ${VERSION} 的部署文件"
	for f in "${files[@]}"; do
		fetch "https://github.com/${REPO}/releases/download/${VERSION}/${f}" "$tmpdir/$f" ||
			die "下载 ${f} 失败；确认版本 ${VERSION} 存在：https://github.com/${REPO}/releases"
	done
fi
for f in "${files[@]}"; do
	install_file "$tmpdir/$f" "$DIR/$f"
done
mkdir -p "$DIR/pgdata" "$DIR/backups" "$DIR/caddy-data"

# .env：数据库密码只在第一次生成。已有数据目录（手工部署过）但没有 .env 时，沿用旧 Compose 的默认密码。
PG_PASSWORD="$(env_get "$ENV_FILE" POSTGRES_PASSWORD)"
if [[ -z "$PG_PASSWORD" ]]; then
	if [[ -n "$(ls -A "$DIR/pgdata" 2>/dev/null)" ]]; then
		PG_PASSWORD="patato"
		warn "pgdata 已有数据但没有 .env，沿用数据库密码 patato；之后可以在数据库和 .env、config.yaml 里一起更换"
	else
		PG_PASSWORD="$(rand_hex 16)"
	fi
fi
touch "$ENV_FILE"
chmod 600 "$ENV_FILE"
env_set "$ENV_FILE" PATATO_VERSION "$VERSION"
env_set "$ENV_FILE" PATATO_IMAGE "$IMAGE"
env_set "$ENV_FILE" PATATO_API_HOST "$API_HOST"
env_set "$ENV_FILE" PATATO_ADMIN_HOST "$ADMIN_HOST"
env_set "$ENV_FILE" PATATO_PORTAL_HOST "$PORTAL_HOST"
env_set "$ENV_FILE" POSTGRES_PASSWORD "$PG_PASSWORD"
env_set "$ENV_FILE" HTTP_PORT "$HTTP_PORT"
env_set "$ENV_FILE" HTTPS_PORT "$HTTPS_PORT"

# config.yaml：只在第一次从示例生成；之后由运营者自己维护
url_for() {
	if [[ "$HTTPS_PORT" == 443 ]]; then printf 'https://%s' "$1"; else printf 'https://%s:%s' "$1" "$HTTPS_PORT"; fi
}
if ((FIRST_INSTALL)); then
	# passkey 的依赖方 ID 必须是 Admin 域名本身或它的上级域名
	rp_id="$ADMIN_HOST"
	admin_url="$(url_for "$ADMIN_HOST")"
	((SINGLE_HOST)) && admin_url="$admin_url/$ADMIN_PATH"
	[[ -n "$DOMAIN" && ("$ADMIN_HOST" == "$DOMAIN" || "$ADMIN_HOST" == *".$DOMAIN") ]] && rp_id="$DOMAIN"
	sed \
		-e "s|^database_url:.*|database_url: \"postgres://patato:${PG_PASSWORD}@postgres:5432/patato?sslmode=disable\"|" \
		-e "s|^secret_key:.*|secret_key: \"$(rand_hex 32)\"|" \
		-e "s|^public_url:.*|public_url: \"$(url_for "$API_HOST")\"|" \
		-e "s|^admin_url:.*|admin_url: \"${admin_url}\"|" \
		-e "s|^portal_url:.*|portal_url: \"$(url_for "$PORTAL_HOST")\"|" \
		-e "s|^  rp_id:.*|  rp_id: \"${rp_id}\"|" \
		"$DIR/config.example.yaml" >"$CONFIG.tmp"
	chmod 600 "$CONFIG.tmp"
	mv "$CONFIG.tmp" "$CONFIG"
	# 镜像里以 uid 10001 运行，需要能读配置
	chown 10001 "$CONFIG" 2>/dev/null || chmod 644 "$CONFIG"
	log "已生成 ${CONFIG}（随机 secret_key 和数据库密码）；Telegram、SMTP、支付等按需再改"
fi

if ((!START)); then
	log "文件已就绪：${DIR}；启动：cd $DIR && docker compose up -d"
	exit 0
fi

# ---------- 启动 / 升级 ----------

if ((!FIRST_INSTALL)) && [[ -n "$(compose ps -q postgres 2>/dev/null)" ]]; then
	backup="$DIR/backups/pre-upgrade-$(date +%Y%m%d-%H%M%S).dump"
	log "升级前备份数据库到 $backup"
	compose exec -T postgres pg_dump -U patato -Fc patato >"$backup" || {
		rm -f "$backup"
		die "备份数据库失败，已停止升级"
	}
fi

if ((PULL)); then
	log "拉取镜像（${IMAGE}:${VERSION}）"
	compose pull
fi
log "启动服务"
compose up -d --remove-orphans

log "等待服务端就绪"
server_id="$(compose ps -q server)"
status=""
for _ in $(seq 1 60); do
	status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$server_id" 2>/dev/null || true)"
	[[ "$status" == healthy ]] && break
	sleep 3
done
if [[ "$status" != healthy ]]; then
	compose logs --tail 50 server >&2 || true
	die "服务端 3 分钟内没有就绪（状态：${status:-未知}）；查看日志：cd $DIR && docker compose logs server"
fi

running="$(compose exec -T server patato-server version 2>/dev/null | tr -d '\r' || true)"
log "Patato ${running:-$VERSION} 已运行"

if ((FIRST_INSTALL)); then
	echo
	compose exec -T server patato-server operator invite -name "$OPERATOR" || warn "生成运营者邀请失败，可以稍后执行：cd $DIR && docker compose exec server patato-server operator invite -name $OPERATOR"
fi

admin_url="$(url_for "$ADMIN_HOST")"
((SINGLE_HOST)) && admin_url="$admin_url/$ADMIN_PATH"
if ((SINGLE_HOST)); then
	hosts_note="域名 ${PORTAL_HOST} 需要解析到这台机器"
	[[ "$API_HOST" != "$PORTAL_HOST" ]] && hosts_note="域名 ${API_HOST}、${PORTAL_HOST} 需要解析到这台机器"
	admin_note="  Admin 地址请妥善保存，不要公开；忘了可以查看 ${CONFIG} 里的 admin_url。
"
else
	hosts_note="三个域名需要解析到这台机器"
	admin_note=""
fi

cat <<EOF

  API：     $(url_for "$API_HOST")
  Admin：   ${admin_url}
  用户侧：  $(url_for "$PORTAL_HOST")
${admin_note}
  安装目录：${DIR}（配置 config.yaml，数据 pgdata/，每日备份 backups/）
  ${hosts_note}，并放行 ${HTTP_PORT}、${HTTPS_PORT} 端口，Caddy 才能签发证书。
  改配置后重启：cd $DIR && docker compose restart server
  升级：再执行一次本脚本（可加 --version 指定版本）
  CLI：curl -fsSL https://github.com/${REPO}/releases/latest/download/install-cli.sh | sh
EOF
