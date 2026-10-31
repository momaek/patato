FROM node:22-bookworm-slim AS web
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

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/admin/dist ./web/admin/dist
COPY --from=web /src/web/portal/dist ./web/portal/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -tags embedweb -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/patato-server ./server/cmd/patato-server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 patato
COPY --from=build /out/patato-server /usr/local/bin/patato-server
USER patato
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["patato-server"]
CMD ["serve"]
