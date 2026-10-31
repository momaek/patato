FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/patato-server ./server/cmd/patato-server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 patato
COPY --from=build /out/patato-server /usr/local/bin/patato-server
USER patato
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["patato-server"]
CMD ["serve"]
