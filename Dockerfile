# 多阶段构建 - 轻量化版
FROM python:3.12-slim-bookworm AS builder

WORKDIR /app

# 只安装编译时需要的依赖
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    git \
    libgeos-dev \
    libspatialindex-dev \
    && rm -rf /var/lib/apt/lists/*

# 复制项目文件
COPY pyproject.toml README.md ./
COPY babeldoc ./babeldoc

# 安装 Python 依赖到指定目录
RUN pip install --upgrade pip && \
    pip install --prefix=/install -e .

# ==================== Go Web 服务构建阶段 ====================
FROM golang:1.24-bookworm AS gobuilder

WORKDIR /build

# 复制 Go 代码和模块文件
COPY web/go.mod web/go.sum* ./
COPY web/main.go ./

# 初始化 go module 并添加依赖
RUN go mod init babeldoc-web 2>/dev/null || true && \
    go get modernc.org/sqlite && \
    go mod tidy

# 编译 Go 应用
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o babeldoc-web .

# ==================== 运行时镜像 ====================
FROM python:3.12-slim-bookworm

WORKDIR /app

# 只安装运行时必需的依赖
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgl1-mesa-glx \
    libglib2.0-0 \
    libsm6 \
    libxext6 \
    libxrender1 \
    libgomp1 \
    libgeos-c1v5 \
    libspatialindex6 \
    poppler-utils \
    && rm -rf /var/lib/apt/lists/*

# 从构建阶段复制安装的包
COPY --from=builder /install /usr/local
COPY --from=builder /app /app

# 从 Go 构建阶段复制 Web 服务
COPY --from=gobuilder /build/babeldoc-web /usr/local/bin/
COPY web/static /app/web/static

# 设置环境变量
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PORT=8080

# 生成并恢复离线资源包（包含所有必需的模型和字体）
RUN babeldoc --generate-offline-assets /tmp/offline-assets && \
    babeldoc --restore-offline-assets /tmp/offline-assets && \
    rm -rf /tmp/offline-assets

# 暴露 Web 服务端口
EXPOSE 8080

# 默认启动 Web 服务
ENTRYPOINT ["/usr/local/bin/babeldoc-web"]
CMD []
