# DocReader Service

DocReader 是 WeKnora 项目中负责文档解析和处理的 gRPC 服务。它支持多种文档格式的读取、OCR 识别、多模态处理等功能。

## Docker Compose 环境变量配置

在 `docker-compose.yml` 文件中，docreader 服务配置了以下环境变量：

```yaml
docreader:
  image: wechatopenai/weknora-docreader:${WEKNORA_VERSION:-latest}
  environment:
    - MINIO_ENDPOINT=minio:9000
    - MINIO_PUBLIC_ENDPOINT=http://localhost:${MINIO_PORT:-9000}
    - MINERU_ENDPOINT=${MINERU_ENDPOINT:-}
    - MAX_FILE_SIZE_MB=${MAX_FILE_SIZE_MB:-}
```

### 环境变量说明

#### 1. MINIO_ENDPOINT

- **说明**: MinIO 服务的内部访问地址（容器间通信）
- **默认值**: `minio:9000`
- **用途**: DocReader 服务使用此地址连接到 MinIO 对象存储服务，用于读取和存储文档处理过程中的文件
- **配置示例**:
  ```yaml
  - MINIO_ENDPOINT=minio:9000  # Docker 网络内部地址
  ```

#### 2. MINIO_PUBLIC_ENDPOINT

- **说明**: MinIO 服务的公开访问地址（外部访问）
- **默认值**: `http://localhost:9000`
- **用途**: 用于生成可从外部访问的文件 URL，例如在文档解析后返回图片链接时使用
- **重要提示**: 
  - 如果需要从其他设备或容器访问，需要将 `localhost` 替换为实际的 IP 地址
  - 可以在 `.env` 文件中配置 `MINIO_PORT` 来自定义端口
- **配置示例**:
  ```bash
  # .env 文件
  MINIO_PORT=9000
  ```
  或直接在 docker-compose.yml 中修改：
  ```yaml
  - MINIO_PUBLIC_ENDPOINT=http://192.168.1.100:9000  # 使用实际 IP
  ```

#### 3. MINERU_ENDPOINT

- **说明**: MinerU 服务的访问地址（可选）
- **默认值**: 空（不使用 MinerU）
- **用途**: MinerU 是一个高级文档解析服务，支持更复杂的文档结构识别和处理。配置此变量后，DocReader 可以调用 MinerU 进行文档解析
- **配置示例**:
  ```bash
  # .env 文件
  MINERU_ENDPOINT=http://mineru-service:8080
  ```

#### 4. MAX_FILE_SIZE_MB

- **说明**: 允许上传的最大文件大小（单位：MB）
- **默认值**: `50` MB
- **用途**: 限制 gRPC 服务接收的文件大小，防止过大的文件导致服务崩溃或性能问题
- **配置示例**:
  ```bash
  # .env 文件
  MAX_FILE_SIZE_MB=100  # 允许最大 100MB 的文件
  ```

## 其他可配置的环境变量

除了 docker-compose.yml 中已配置的变量外，DocReader 还支持以下环境变量（可根据需要添加）：

### gRPC 配置

- `DOCREADER_GRPC_MAX_WORKERS`: gRPC 服务的最大工作线程数（默认：4）。
  pdfium（pypdfium2）默认构建非线程安全，DocReader 会在单进程内串行所有 pdfium 访问，
  因此该值 >1 是安全的，**不再需要为规避并发问题而强制设为 1**；提高该值可让健康检查、
  ListEngines 等轻量请求与小文档在有大 PDF 在解析时仍能得到处理。
- `DOCREADER_GRPC_PORT`: gRPC 服务监听端口（默认：50051）

### 解析器资源控制

- `DOCREADER_PDFIUM_SERIALIZE`: 是否在单进程内串行所有 pdfium 访问（默认：true）。
  pdfium/pypdfium2 默认构建非线程安全，请保持 true；仅在确知使用线程安全的 pdfium
  构建时才可设为 false。
- `DOCREADER_MARKITDOWN_MAX_WORKERS`: MarkItDown 解析的最大并发数（默认：1，设为 0 可关闭限流）
- `DOCREADER_PDF_RENDER_MAX_WORKERS`: 扫描 PDF 渲染为图片的最大并发数（默认：1，设为 0 可关闭限流）
- `DOCREADER_PDF_RENDER_DPI`: 扫描 PDF 渲染 DPI（默认：200）
- `DOCREADER_PDF_JPEG_QUALITY`: 扫描 PDF 输出 JPEG 质量（默认：90，范围会自动限制在 1-95）
- `DOCREADER_DOCX_MAX_WORKERS`: DOCX 多进程解析的最大并发数（默认：1，设为 0 可关闭限流）
- `DOCREADER_WEB_SCRAPE_MAX_WORKERS`: 网页抓取（Playwright）的最大并发数（默认：1，设为 0 可关闭限流）

### OCR / VLM

DocReader 自身不再内置 OCR 与 VLM 后端。扫描 PDF 会被渲染为 JPEG 图片后交由 Go App 侧调用 OCR/VLM 服务处理，相关配置请参考主项目文档。

### 存储配置

DocReader 支持多种存储后端：

#### MinIO/S3 存储（推荐）

- `STORAGE_TYPE`: 设置为 `minio`
- `MINIO_ACCESS_KEY_ID`: MinIO 访问密钥 ID（默认：minioadmin）
- `MINIO_SECRET_ACCESS_KEY`: MinIO 访问密钥（默认：minioadmin）
- `MINIO_BUCKET_NAME`: MinIO 存储桶名称（默认：WeKnora）
- `MINIO_PATH_PREFIX`: 文件路径前缀
- `MINIO_USE_SSL`: 是否使用 SSL（默认：false）

#### 腾讯云 COS 存储

- `STORAGE_TYPE`: 设置为 `cos`
- `COS_SECRET_ID`: COS 访问密钥 ID
- `COS_SECRET_KEY`: COS 访问密钥
- `COS_REGION`: COS 区域
- `COS_BUCKET_NAME`: COS 存储桶名称
- `COS_APP_ID`: COS 应用 ID
- `COS_PATH_PREFIX`: 文件路径前缀
- `COS_ENABLE_OLD_DOMAIN`: 是否使用旧域名（默认：true）

#### 阿里云 OSS 存储

- `STORAGE_TYPE`: 设置为 `oss`
- `OSS_ACCESS_KEY_ID`: OSS 访问密钥 ID
- `OSS_ACCESS_KEY_SECRET`: OSS 访问密钥
- `OSS_ENDPOINT`: OSS 端点（如 `oss-cn-hangzhou.aliyuncs.com`）
- `OSS_BUCKET_NAME`: OSS 存储桶名称
- `OSS_REGION`: OSS 区域（如 `cn-hangzhou`）
- `OSS_PATH_PREFIX`: 文件路径前缀

### 代理配置

如果需要通过代理访问外部服务：

- `EXTERNAL_HTTP_PROXY`: HTTP 代理地址
- `EXTERNAL_HTTPS_PROXY`: HTTPS 代理地址

### 图像处理配置

扫描 PDF 会被渲染为 JPEG 图片后交给 Go App 侧 OCR 处理。如果在导入多个大 PDF 时出现资源占用过高，
可以优先调低 `DOCREADER_PDF_RENDER_MAX_WORKERS` 或 `DOCREADER_MARKITDOWN_MAX_WORKERS`。

## 配置示例

### 基础配置（使用 MinIO）

```yaml
docreader:
  environment:
    - MINIO_ENDPOINT=minio:9000
    - MINIO_PUBLIC_ENDPOINT=http://localhost:9000
    - MAX_FILE_SIZE_MB=50
```

### 高级配置（启用 MinerU）

```yaml
docreader:
  environment:
    - MINIO_ENDPOINT=minio:9000
    - MINIO_PUBLIC_ENDPOINT=http://192.168.1.100:9000
    - MINERU_ENDPOINT=http://mineru:8080
    - MAX_FILE_SIZE_MB=100
```

### 使用腾讯云 COS

```yaml
docreader:
  environment:
    - STORAGE_TYPE=cos
    - COS_SECRET_ID=your_secret_id
    - COS_SECRET_KEY=your_secret_key
    - COS_REGION=ap-guangzhou
    - COS_BUCKET_NAME=your-bucket
    - COS_APP_ID=your_app_id
    - MAX_FILE_SIZE_MB=50
```

### 使用阿里云 OSS

```yaml
docreader:
  environment:
    - STORAGE_TYPE=oss
    - OSS_ACCESS_KEY_ID=your_access_key_id
    - OSS_ACCESS_KEY_SECRET=your_access_key_secret
    - OSS_ENDPOINT=oss-cn-hangzhou.aliyuncs.com
    - OSS_BUCKET_NAME=your-bucket
    - OSS_REGION=cn-hangzhou
    - MAX_FILE_SIZE_MB=50
```

## 常见问题

### 1. DocReader 服务无法启动？

检查容器日志中是否存在依赖缺失或权限相关错误，必要时确认 `MINIO_ENDPOINT` / 存储相关环境变量是否正确配置。

### 2. 图片无法显示？

检查 `MINIO_PUBLIC_ENDPOINT` 配置：
- 确保使用的是可从浏览器访问的地址
- 如果从其他设备访问，不要使用 `localhost`，应使用实际 IP 地址

### 3. 文件上传失败？

检查 `MAX_FILE_SIZE_MB` 配置，确保限制足够大。同时需要确保前端和后端服务的文件大小限制保持一致。

## 服务健康检查

DocReader 服务配置了健康检查：

```yaml
healthcheck:
  test: ["CMD", "grpc_health_probe", "-addr=localhost:50051"]
  interval: 30s
  timeout: 10s
  retries: 3
  start_period: 60s
```

可以通过以下命令检查服务状态：

```bash
docker compose ps docreader          # 列出全部副本
docker compose logs docreader        # 聚合所有副本日志
docker compose exec docreader sh     # 进入某个副本（会任选一个）
```

## 水平扩容（多副本）

docreader 是无状态解析器，`docker compose` 部署可直接水平扩容。各副本持有独立的
pdfium 实例——这是**纯 PDF 批量吞吐的唯一扩容手段**：单容器内所有 pdfium 调用都会被
`DOCREADER_PDFIUM_SERIALIZE` 串行化，调大 `DOCREADER_GRPC_MAX_WORKERS` 无效。

```bash
# 方式一：在 .env 设置副本数后启动
DOCREADER_REPLICAS=4 docker compose up -d

# 方式二：直接 scale（与方式一选其一，不要同时使用）
docker compose up -d --scale docreader=4
```

Go App 端无需改动：客户端以 `dns:///<addr>` + `round_robin` 拨号，Docker 内置 DNS 会把
服务名 `docreader` 解析为全部副本 IP，自动在副本间负载均衡。

扩容时注意：

- **同步调小每副本并行度**，令
  `replicas * DOCREADER_PDF_RENDER_MAX_WORKERS * DOCREADER_PDF_RENDER_PARALLELISM <= CPU 核数`，
  否则嵌套进程池会超订反而更慢。
- **内存**：每个副本会常驻其在途文档（含渲染出的页图），副本数受 RAM 限制。
- **不要发布宿主端口**：`ports:` 映射在副本数 > 1 时会冲突（docreader 默认只用 `expose`，已在 WeKnora-network 内可达）。
- 单机多副本只提升核心利用率、不增加算力，也不提供跨机高可用；好处之一是单个副本若因 pdfium 崩溃（SIGABRT）被拉起时，其余副本继续服务。

实测参考（8 核单机，模拟副本）：文本 PDF 4 副本 3.4x / 8 副本 4.6x；扫描件 8 副本且每副本
`DOCREADER_PDF_RENDER_PARALLELISM=1` 达 5.1x。

## 更多信息

- 服务端口：50051（gRPC）
- 容器名称：不固定（为支持扩容已移除 `container_name`），用 `docker compose ps docreader` 查看
- 网络：WeKnora-network
- 重启策略：unless-stopped
