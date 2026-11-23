# 前端产物与架构无关、Go 交叉编译不需要 cgo：这两个阶段在构建机原生架构上运行，
# 多架构镜像（linux/amd64、linux/arm64）只有最后一层按目标架构取基础镜像，不用 QEMU 模拟编译。
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS web
WORKDIR /src/web
RUN npm install --global pnpm@10.32.1
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
COPY web/admin/package.json ./admin/package.json
COPY web/portal/package.json ./portal/package.json
COPY web/packages/api/package.json ./packages/api/package.json
COPY web/packages/ui/package.json ./packages/ui/package.json
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/admin/dist ./web/admin/dist
COPY --from=web /src/web/portal/dist ./web/portal/dist
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags embedweb -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/patato-server ./server/cmd/patato-server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 patato
COPY --from=build /out/patato-server /usr/local/bin/patato-server
USER patato
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["patato-server"]
CMD ["serve"]
