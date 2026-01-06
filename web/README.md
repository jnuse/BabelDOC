# BabelDOC Web 服务

这是一个用 Go 编写的简单 Web 服务，为 BabelDOC PDF 翻译工具提供 Web 界面。

## 功能特性

- 📤 通过浏览器上传 PDF 文件
- 🌐 Web 界面配置翻译参数
- 🔄 自动调用 babeldoc 进行翻译
- 📥 翻译完成后直接下载结果
- 🔧 支持 OpenAI API 配置

## 使用 Docker 运行

### 构建镜像

```bash
cd /root/trans/BabelDOC
docker build -t babeldoc-web .
```

### 运行容器

```bash
docker run -d \
  -p 8080:8080 \
  --name babeldoc-web \
  babeldoc-web
```

### 访问服务

在浏览器中打开: http://localhost:8080

## 使用本地 Go 运行（开发模式）

### 前置要求

- Go 1.21+
- Python 3.12+
- 已安装 babeldoc

### 运行服务

```bash
cd /root/trans/BabelDOC/web
go run main.go
```

服务将在 http://localhost:8080 启动

## API 端点

### 上传并翻译文件

**POST** `/api/upload`

**表单参数:**
- `file` (文件): PDF 文件
- `lang_in` (字符串): 源语言代码（默认: en）
- `lang_out` (字符串): 目标语言代码（默认: zh）
- `pages` (字符串): 页码范围（可选）
- `api_key` (字符串): OpenAI API Key（可选）
- `model` (字符串): OpenAI 模型（可选）
- `base_url` (字符串): OpenAI Base URL（可选）

**响应:**
```json
{
  "success": true,
  "downloads": [
    "/api/download/20060102-150405/output.pdf"
  ]
}
```

### 下载翻译结果

**GET** `/api/download/{timestamp}/{filename}`

返回翻译后的 PDF 文件。

### 健康检查

**GET** `/api/status`

**响应:**
```json
{
  "status": "ok",
  "version": "1.0"
}
```

## 环境变量

- `PORT`: Web 服务监听端口（默认: 8080）

## 支持的语言

- `en`: 英语
- `zh`: 中文
- `ja`: 日语
- `ko`: 韩语
- `fr`: 法语
- `de`: 德语
- `es`: 西班牙语
- `ru`: 俄语

更多语言请参考 BabelDOC 文档。

## 文件存储

- 上传的文件存储在: `/tmp/babeldoc/uploads`
- 翻译结果存储在: `/tmp/babeldoc/outputs/{timestamp}`

## 限制

- 最大上传文件大小: 100 MB
- 仅支持 PDF 文件格式

## 故障排除

### 翻译失败

1. 检查 babeldoc 是否正确安装
2. 检查 Docker 容器日志: `docker logs babeldoc-web`
3. 确保提供了有效的 OpenAI API Key（如果使用 OpenAI）

### 文件下载失败

1. 检查输出目录权限
2. 确认翻译完成且生成了输出文件

## 许可证

与 BabelDOC 主项目保持一致。
