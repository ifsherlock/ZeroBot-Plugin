# ZeroBot-Plugin mediaparser fork

> 本仓库是基于 `FloatTech/ZeroBot-Plugin` 的个人自用 fork，重点维护 `plugin/mediaparser` 聚合媒体解析插件。

- 源项目地址：[FloatTech/ZeroBot-Plugin](https://github.com/FloatTech/ZeroBot-Plugin)
- 原 README 文件：[ifsherlock/ZeroBot-Plugin README](https://github.com/ifsherlock/ZeroBot-Plugin/blob/master/README.md)

## 这个 fork 做了什么

本 fork 主要围绕 `mediaparser` 做轻量运行时和 Docker 部署适配，用于在 llbot/OneBot v11 或官方 QQBot 场景下解析常见平台链接，并生成适合聊天窗口展示的图文卡片或媒体消息。

当前重点能力：

- 多平台链接识别和解析。
- 图文、视频、动态、文章、商品、游戏页面的信息卡片生成。
- 媒体图片和视频的按需发送。
- 群聊、用户、平台维度的开关和黑白名单。
- 内置轻量 WebUI，用于配置解析插件和运行参数。
- Docker 镜像内置运行所需的 ZeroBot-Plugin 与轻量静态服务，运行数据统一持久化到 `data/`。

## mediaparser 平台能力

当前支持的平台和链接形态如下。表格只描述本 fork 已接入的解析入口；具体能否拿到原图、高清视频或完整正文，仍会受平台登录态、地区、风控和源站页面变化影响。

<table>
<thead>
<tr>
<th align="center">平台</th>
<th>支持的链接形态</th>
<th align="center">支持能力</th>
</tr>
</thead>
<tbody>
<tr>
<td align="center"><strong>哔哩哔哩</strong></td>
<td>短链（<code>b23.tv/...</code>、<code>bili2233.cn/...</code>）<br>视频链接（<code>bilibili.com/video/BV...</code>、<code>acg.tv/...</code>）<br>番剧链接、动态链接（<code>bilibili.com/opus/...</code>）</td>
<td align="center">视频 / 图文 / 动态 / 番剧</td>
</tr>
<tr>
<td align="center"><strong>抖音</strong></td>
<td>短链（<code>v.douyin.com/...</code>）<br>视频链接、图集链接（<code>douyin.com/video/...</code>、<code>douyin.com/note/...</code>）</td>
<td align="center">视频 / 图集 / 文本</td>
</tr>
<tr>
<td align="center"><strong>TikTok</strong></td>
<td>短链（<code>vm.tiktok.com/...</code>、<code>vt.tiktok.com/...</code>）<br>视频链接、图集链接（<code>tiktok.com/@.../video/...</code>、<code>tiktok.com/@.../photo/...</code>）</td>
<td align="center">视频 / 图集 / 文本</td>
</tr>
<tr>
<td align="center"><strong>快手</strong></td>
<td>短链（<code>v.kuaishou.com/...</code>）<br>作品链接（<code>kuaishou.com/...</code>、<code>gifshow.com/...</code>、<code>chenzhongtech.com/...</code>）</td>
<td align="center">视频 / 图集 / 文本</td>
</tr>
<tr>
<td align="center"><strong>微博</strong></td>
<td>微博正文（<code>weibo.com/...</code>、<code>m.weibo.cn/detail/...</code>、<code>weibo.cn/status/...</code>）<br>微博视频（<code>video.weibo.com/...</code>）</td>
<td align="center">视频 / 图片 / 文本</td>
</tr>
<tr>
<td align="center"><strong>小红书</strong></td>
<td>短链（<code>xhslink.com/...</code>）<br>笔记链接（<code>xiaohongshu.com/explore/...</code>、<code>xiaohongshu.com/discovery/item/...</code>）</td>
<td align="center">视频 / 图集 / 文本</td>
</tr>
<tr>
<td align="center"><strong>闲鱼</strong></td>
<td>短链（<code>m.tb.cn/...</code>）<br>商品页（<code>goofish.com/item...</code>、<code>2.taobao.com/...</code>、<code>market.m.taobao.com/...</code>）</td>
<td align="center">商品 / 图片 / 文本</td>
</tr>
<tr>
<td align="center"><strong>AcFun</strong></td>
<td>视频链接（<code>acfun.cn/v/...</code>、<code>m.acfun.cn/v/...</code>）</td>
<td align="center">视频 / 文本</td>
</tr>
<tr>
<td align="center"><strong>YouTube</strong></td>
<td>视频链接（<code>youtube.com/watch...</code>、<code>youtu.be/...</code>、<code>music.youtube.com/...</code>）</td>
<td align="center">视频 / 文本</td>
</tr>
<tr>
<td align="center"><strong>Instagram</strong></td>
<td>帖子、Reels、分享链接（<code>instagram.com/...</code>）</td>
<td align="center">视频 / 图片 / 文本</td>
</tr>
<tr>
<td align="center"><strong>今日头条</strong></td>
<td>文章、视频、微头条链接（<code>toutiao.com/...</code>、<code>snssdk.com/...</code>）</td>
<td align="center">视频 / 图片 / 文章</td>
</tr>
<tr>
<td align="center"><strong>小黑盒</strong></td>
<td>游戏详情、BBS 分享链接（<code>xiaoheihe.cn/...</code>、<code>heybox.cn/...</code>）</td>
<td align="center">视频 / 图片 / 帖子 / 游戏</td>
</tr>
<tr>
<td align="center"><strong>Twitter/X</strong></td>
<td>推文链接（<code>twitter.com/.../status/...</code>、<code>x.com/.../status/...</code>）<br>兼容 <code>fxtwitter.com</code>、<code>fixupx.com</code>、<code>vxtwitter.com</code></td>
<td align="center">视频 / 图片 / 文本</td>
</tr>
<tr>
<td align="center"><strong>Keylol</strong></td>
<td>论坛帖子（<code>keylol.com/t...</code>、<code>thread-...</code>、<code>forum.php?mod=viewthread...</code>）</td>
<td align="center">帖子 / 图片 / 视频嵌入 / Steam 卡片</td>
</tr>
<tr>
<td align="center"><strong>Steam</strong></td>
<td>商店页面（<code>store.steampowered.com/app/...</code>）</td>
<td align="center">游戏卡片 / 价格 / 评价 / 封面</td>
</tr>
</tbody>
</table>

## Docker 部署方式

仓库提供两种 compose 用法：

- `docker-compose.yaml`：本地源码构建镜像，适合你修改代码后自己构建运行。
- `docs/docker-compose.mediaparser.yml`：直接使用预构建镜像，适合只部署 mediaparser 运行环境。

两种方式都会把容器内 `/app/data` 挂载到宿主机 `./data`，配置、Cookie、缓存图片、WebUI 数据都会保存在这里。不要把账号、Token、Cookie、AppSecret 等敏感内容提交到 Git。

### 使用预构建镜像

适合普通部署或只想使用 WebUI / mediaparser 的场景：

```bash
docker compose -f docs/docker-compose.mediaparser.yml up -d
```

更新镜像：

```bash
docker compose -f docs/docker-compose.mediaparser.yml pull
docker compose -f docs/docker-compose.mediaparser.yml up -d
```

这个文件默认使用：

- 镜像：`jaysherlock/zerobot-plugin:latest`
- 服务名：`mediaparser`
- 容器名：`mediaparser`
- 网络：`host`
- 数据目录：`./data:/app/data`
- `ONEBOT_WS_URL` 默认留空，适合先只打开 WebUI 做配置。

如果要连接同机 llbot，可把 `ONEBOT_WS_URL` 改成 llbot 的反向 WebSocket 地址，例如 `ws://127.0.0.1:3001`。

### 使用本地源码构建

适合改过源码或想使用当前工作区内容构建镜像：

```bash
docker compose up -d --build
```

这个文件默认使用：

- 构建上下文：当前仓库根目录。
- 镜像名：`zerobot-plugin:local`
- 服务名：`zerobot-plugin`
- 容器名：`zerobot-plugin`
- 网络：`host`
- 数据目录：`./data:/app/data`
- `ONEBOT_WS_URL` 默认指向 `ws://127.0.0.1:3001`。

修改插件源码或 `main.go` 里的插件 import 后，重新构建：

```bash
docker compose build --no-cache zerobot-plugin
docker compose up -d zerobot-plugin
```

## 端口说明

| 端口 | 用途 | 说明 |
| :---: | :--- | :--- |
| `3000` | WebUI | 用于配置 mediaparser、QQBot、名单、平台开关等 |
| `3088` | 静态缓存服务 | 用于读取 mediaparser 生成的卡片和图片缓存 |

默认 compose 使用 `network_mode: host`，容器会直接监听宿主机端口。如果你改成桥接网络，需要自行添加端口映射：

```yaml
ports:
  - "3000:3000"
  - "3088:3088"
```

## 环境变量说明

| 变量 | 默认值 | 用途 |
| :--- | :--- | :--- |
| `TZ` | `Asia/Shanghai` | 容器时区，影响日志和定时任务时间 |
| `WEBUI_ADDR` | `0.0.0.0:3000` | WebUI 监听地址 |
| `ONEBOT_WS_URL` | 视 compose 文件而定 | OneBot / llbot 反向 WebSocket 地址；不用 OneBot 时可留空 |
| `ONEBOT_WS_TOKEN` | 空 | OneBot WebSocket 鉴权 token；未启用鉴权时留空 |
| `ONEBOT_DATA_DIR` | `${PWD}/data` | OneBot / llbot 可见的数据目录，用于本地文件路径映射 |
| `BOT_NICKNAME` | `ZeroBot` | 机器人默认昵称 |
| `COMMAND_PREFIX` | `/` | 命令前缀 |
| `SUPER_USERS` | 空 | 超级用户 QQ 号，多个账号用空格分隔 |
| `ZBP_ARGS` | 空 | 额外启动参数，例如 `-d` 开启 debug 日志 |

常见配置建议：

- 只使用 WebUI 或官方 QQBot 解析能力：`ONEBOT_WS_URL` 留空。
- llbot 和本服务在同一台 Linux 主机：`ONEBOT_WS_URL` 填 llbot 实际监听地址。
- 不需要本地 `file://` 路径映射时：`ONEBOT_DATA_DIR` 可以留空。

## 数据目录说明

运行时只需要持久化 `./data`，对应容器内 `/app/data`。

| 路径 | 用途 |
| :--- | :--- |
| `./data` | 宿主机持久化目录 |
| `/app/data` | 容器内数据目录 |
| `/app/data/mediaparser` | mediaparser 配置、缓存和运行数据 |
| `/app/data/mediaparser/cache` | 解析卡片和图片缓存 |

备份或迁移时，优先处理 `./data`。镜像本身可以随时重新拉取或重新构建。

## WebUI 与常用操作

启动后打开 WebUI：

```text
http://127.0.0.1:3000
```

在 WebUI 中可以配置：

- 聚合解析的总开关、调试日志、输出方式。
- 各平台开关。
- 群聊、私聊、群成员名单。
- Cookie、视频大小限制、缓存时间等运行参数。
- 官方 QQBot 相关参数。

常用命令：

```bash
docker compose logs -f zerobot-plugin
docker compose restart zerobot-plugin
docker compose down
```

如果使用预构建 compose，把服务名换成 `mediaparser`：

```bash
docker compose -f docs/docker-compose.mediaparser.yml logs -f mediaparser
docker compose -f docs/docker-compose.mediaparser.yml restart mediaparser
docker compose -f docs/docker-compose.mediaparser.yml down
```

检查缓存服务是否启动：

```bash
curl http://127.0.0.1:3088/healthz
```

## 说明

这个 fork 以个人部署和自用适配为目标，不保证所有环境通用，也不代表上游项目的正式发行版本。上游 ZeroBot-Plugin 的完整插件列表、命令说明和历史文档，请参考上面的源项目与原 README 链接。
