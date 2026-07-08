# 阶段 1：编译
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/ccp-proxy ./cmd/ccp-proxy
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/ccp ./cmd/ccp

# 阶段 2：运行
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/ccp-proxy /out/ccp /usr/local/bin/
RUN mkdir -p /root/.cc_proxy
EXPOSE 8787
ENTRYPOINT ["ccp-proxy"]
