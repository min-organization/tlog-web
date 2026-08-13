# ---- 阶段 1：构建前端 (Vue3 + Vite + Element Plus) ----
FROM node:22-alpine AS fe
WORKDIR /fe
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install --legacy-peer-deps --registry=https://registry.npmmirror.com
COPY frontend/ ./
RUN npm run build

# ---- 阶段 2：构建 Go 单二进制（embed 前端 dist） ----
FROM golang:1.23 AS builder
ENV CGO_ENABLED=0 GOOS=linux GOPROXY=https://goproxy.cn,direct
WORKDIR /app
COPY backend/ ./backend/
COPY --from=fe /fe/dist ./backend/frontend/dist
RUN cd backend && go mod tidy && go build -ldflags="-s -w" -o /tlog-web-server .

# ---- 阶段 3：运行 ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /
COPY --from=builder /tlog-web-server /tlog-web-server
# /data 挂持久卷（bind mount）：SQLite 索引 + collector 断点状态
RUN mkdir -p /data
EXPOSE 8080
CMD ["/tlog-web-server"]
