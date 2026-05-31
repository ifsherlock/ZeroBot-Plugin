# Docker 部署说明

这个镜像把 ZeroBot-Plugin 和一个轻量 nginx 放在同一个容器里：

- `3000`：内置 WebUI。
- `3088`：nginx 静态服务，只公开 `/app/data/mediaparser/cache` 下的解析卡片和图片缓存。
- `/app/data`：唯一需要持久化的数据目录，WebUI 保存的 QQBot AppID/AppSecret、白名单、Cookie、缓存都在这里。

不要把 IP、账号、密码、Cookie、AppSecret 写进 Dockerfile、compose 文件或提交到 Git。生产配置请在 WebUI 里保存，或放在本机未提交的挂载目录中。

## QQBot 视频说明

官方 QQBot 不是 OneBot 那种本地 `file://xxx.mp4` 直接发送模式。视频通常要走官方媒体上传/审核/格式限制流程，和现在的 Markdown 图片、公网图片 URL 不是一套能力。

当前阶段默认支持：

- 聚合解析卡片：渲染成 PNG，通过 `3088` 的公网 URL 发给 QQBot。
- 媒体图片下载：可在 WebUI 打开，一张一张图片发送。
- 视频：先保留为后续阶段，不在 QQBot 通道默认发送。

如果需要完整视频体验，建议先用 llbot/OneBot WS 通道；官方 QQBot 通道先跑卡片和图片。

## 使用 GitHub Actions 自动构建镜像

仓库里的 `.github/workflows/dockerhub.yml` 会在 GitHub Actions 中完成完整镜像构建流程：

1. `go generate main.go` 生成编译期需要的 `abineundo/ref/**` 临时引用文件。
2. 构建 `linux/amd64` 和 `linux/arm64` 的 Go 二进制做校验，产物只留在 CI 临时目录，不上传 artifact，也不会提交到仓库。
3. 使用 `Dockerfile` 通过 Buildx 构建 `linux/amd64,linux/arm64` 多架构镜像。
4. 推送到 DockerHub：`jaysherlock/zerobot-plugin`。

需要在 GitHub 仓库里设置两个 Actions Secrets：

- `DOCKERHUB_USERNAME`：DockerHub 用户名，例如 `jaysherlock`。
- `DOCKERHUB_TOKEN`：DockerHub Access Token。

设置路径：`Settings` -> `Secrets and variables` -> `Actions` -> `New repository secret`。

触发规则：

- 推送到 `master`：发布 `latest` 和 `sha-<commit>` 标签。
- 推送 `v*` tag：发布对应 tag 标签。
- 也可以在 Actions 页面手动运行 `DockerHub Image` workflow。

## 只使用媒体解析服务

如果你只需要内置 WebUI 和媒体解析能力，不想在服务器上本地构建镜像，可以直接使用 GitHub Actions 自动发布到 DockerHub 的预构建镜像：

```bash
docker compose -f docs/docker-compose.mediaparser.yml up -d
```

更新镜像：

```bash
docker compose -f docs/docker-compose.mediaparser.yml pull
docker compose -f docs/docker-compose.mediaparser.yml up -d
```

示例文件见 `docs/docker-compose.mediaparser.yml`。它默认使用 `jaysherlock/zerobot-plugin:latest`，并把 `ONEBOT_WS_URL` 留空，适合只打开 WebUI 做解析配置或只使用官方 QQBot/解析能力的场景。

如果后续要对接同机 llbot，把 `ONEBOT_WS_URL` 改成 `ws://127.0.0.1:3001`；如果 llbot 在其他机器，改成对应地址。

## 一键启动

在项目根目录执行：

```bash
docker compose up -d --build
```

默认 compose 使用 `network_mode: host`，适合同一台 Linux 服务器上运行 llbot：

- WebUI：`http://服务器IP:3000`
- QQBot 图片公网根地址：`http://服务器IP:3088/cache/`
- llbot WS：默认 `ws://127.0.0.1:3001`

启动后进入 WebUI：

1. 打开「官方 QQBot」页面。
2. 填 `AppID`、`AppSecret`、事件订阅所需 OpenID。
3. 把「公网图片根地址」填成 `http://你的公网域名或IP:3088/cache/`。
4. 保存后点右上角重启按钮，或执行 `docker compose restart zerobot-plugin`。

## 对接 llbot

默认配置：

```yaml
ONEBOT_WS_URL: ws://127.0.0.1:3001
```

如果 llbot 不在同一台机器，改成对应地址：

```yaml
ONEBOT_WS_URL: ws://llbot-host:3001
```

如果你不用 host 网络，改为桥接模式时可以这样写：

```yaml
services:
  zerobot-plugin:
    ports:
      - "3000:3000"
      - "3088:3088"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    environment:
      ONEBOT_WS_URL: ws://host.docker.internal:3001
```

桥接模式下删除 `network_mode: host`。

## 常用命令

```bash
docker compose logs -f zerobot-plugin
docker compose restart zerobot-plugin
docker compose pull
docker compose up -d --build
docker compose down
```

验证 nginx 缓存服务：

```bash
curl http://127.0.0.1:3088/healthz
```

## 添加或删除插件后怎么编译

这个仓库是编译期插件模式。改插件源码或在 `main.go` 里调整 import 后，重新构建镜像：

```bash
docker compose build --no-cache zerobot-plugin
docker compose up -d zerobot-plugin
```

默认镜像只拷贝媒体解析需要的内置 Logo。运行时下载的字体、缓存、Cookie 和 WebUI 配置都进入 `./data`，不会进入镜像层。

## 更小镜像的取舍

当前运行层基于 Alpine，只安装 `ca-certificates`、`tzdata`、`nginx`，Go 二进制使用 `CGO_ENABLED=0` 和 `-s -w` 去符号构建。没有内置 ffmpeg、yt-dlp 或浏览器组件，所以镜像更小；如果你后面要在容器里启用视频下载/转封装，再单独派生一个带 ffmpeg 的镜像。
