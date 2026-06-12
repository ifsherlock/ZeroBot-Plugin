# ZeroBot-Plugin 项目 Agent 规则

## 基本沟通

- 面向用户默认使用中文，先给结果，再补充必要证据。
- 执行命令、读写文件、测试页面、查看日志时，尽量使用简洁进度块：

```text
> 🔧 步骤：正在做什么
> 🎯 目的：为什么做
> ▶️ 执行：命令、页面、文件或操作
> ✅ 结果：当前状态
> 🔎 证据：可验证路径或命令结果
> 📝 备注：可选，最多一句
```

- 用户偏好实用闭环：能实现就直接实现，能验证就直接验证；只有缺少信息会显著改变方案或带来真实风险时才询问。
- 多步骤、机械、耗时流程优先交给子 agent 或并行工具处理，但最终结论要自己复核。

## 仓库与分支

- 当前仓库是 `ZeroBot-Plugin`，主要自定义开发集中在 `plugin/mediaparser`。
- 分支默认使用 `codex/` 前缀；MediaShield 开发分支为 `codex/mediashield`。
- 提交信息默认使用中文。
- 每次 `git add`、`git commit`、`git push` 前必须检查：
  - `git status --short --branch`
  - `git diff --stat`
  - 实际 diff 内容
- 严禁提交或推送：
  - 内部开发 Markdown、项目计划草稿、私有测试记录。
  - 用户本地 IP、账号、密码、Cookie、Token、AppSecret、真实 QQ 号完整列表。
  - 本地编译二进制、临时构建目录、Docker 构建产物、缓存文件、日志文件。
- 不要回滚用户已有改动；遇到无关脏文件只忽略，除非它影响当前任务。

## 项目插件结构

- MediaParser 插件目录：`plugin/mediaparser`。
- 主入口：`plugin/mediaparser/mediaparser.go`。
- 主要功能文件：
  - `mediaparser.go`：插件注册、配置结构、命令入口、自动解析主流程。
  - `safety.go`：内容安全、分类屏蔽词、排除词、迁移和命令处理。
  - `mediashield.go`：X 平台 MediaShield 打码预览、加密压缩包、密码回复和发送链路。
  - `webui.go`：插件 WebUI、API、Basic Auth、配置页面。
  - `system.go`：WebUI/OneBot/QQBot 等系统设置读写。
  - `twitter.go`：X/Twitter 解析和敏感标记处理。
  - `qqbot_driver.go`：官方 QQBot 私有驱动能力，属于非合规私有能力；提交范围要谨慎。
- 平台解析文件按平台拆分，例如 `bilibili.go`、`douyin.go`、`instagram.go`、`tiktok.go`、`youtube` 相关逻辑在对应平台或 yt-dlp 分支中。
- 静态 logo 位于 `plugin/mediaparser/data/mediaparser/logos`。

## 插件注册流程

- ZeroBot 插件通过 `control.AutoRegister(&ctrl.Options[*zero.Ctx]{...})` 注册。
- MediaParser 当前注册在 `plugin/mediaparser/mediaparser.go` 的全局 `engine`：
  - `DisableOnDefault: false`
  - `PrivateDataFolder: "mediaparser"`
  - `Brief` / `Help` 用于插件说明和命令帮助。
- `init()` 中完成运行时初始化：
  - `configPath = filepath.Join(engine.DataFolder(), "config.json")`
  - `cacheDir = filepath.Join(engine.DataFolder(), "cache")`
  - 创建缓存目录、读取配置。
  - 注册管理命令：`zero.OnCommand("媒体解析", zero.AdminPermission).SetBlock(true).Handle(handleCommand)`
  - 注册状态命令：`zero.OnFullMatch("媒体解析状态", zero.AdminPermission)...`
  - 注册自动解析：`zero.OnMessage().SetBlock(false).Handle(handleAutoParse)`
  - 启动 WebUI：`startPluginWebUI()`
- 新增命令时优先接入 `handleCommand` 子命令，不要散落多套入口。
- 新增自动触发行为时接入 `handleAutoParse` / `processLink` 链路，并保持平台、群、私聊权限判断一致。

## 配置与数据

- MediaParser 主配置：`engine.DataFolder()/config.json`，对应 `config` 结构体。
- 系统配置：`engine.DataFolder()/system.json`，对应 `SystemSettings`。
- WebUI 认证配置：`engine.DataFolder()/webui_auth.json`。
- 缓存目录：`engine.DataFolder()/cache`。
- 所有配置修改必须走规范化函数和保存函数：
  - `loadConfig`
  - `normalizeConfig`
  - `saveConfig`
  - `saveConfigLocked`
  - `normalizeSystemSettings`
  - `saveSystemSettings`
- 新增配置字段要同时考虑：
  - `config` 结构体 JSON 字段。
  - 默认值。
  - `normalizeConfig` 兼容旧配置。
  - WebUI 读取、保存和显示。
  - 单元测试或回归测试。
- 旧配置迁移优先做幂等迁移，不要要求用户手工删除配置。

## MediaParser 主流程

- 自动解析入口：`handleAutoParse`。
- 链接解析入口：`processLink`。
- 流程大致为：
  - 提取链接并识别平台。
  - 检查总开关、平台开关、黑白名单、群/私聊访问策略。
  - 调用平台解析器获取 `mediaMeta`。
  - 内容安全检查。
  - MediaShield 接管判断。
  - 发送信息卡、媒体或降级文本。
- 内容安全应在解析成功后、任何卡片/媒体发送前生效。
- MediaShield 仅限 X/Twitter，默认总开关关闭，并且需要群开关或私聊白名单允许。

## 内容安全规则

- 内置分类保持小规模、高置信，避免正常讨论误杀。
- 当前分类聚合为：
  - `adult`：色情/成人。
  - `ad`：引流广告。
  - `violence`：暴恐/极端暴力。
  - `politics`：政治敏感；WebUI 中隐藏或谨慎展示。
- 内置词使用 Base64 存在仓库，通过 `decodeSafetyBuiltinWords` 解码；不要在公开文件中明文打印敏感词库。
- 自定义分类通过 `SafetyCustomCategories` 管理，支持普通词、`*` / `?` 通配、`re:` 正则。
- 排除词优先用于处理误杀。
- 默认策略以谨慎为主：内置词不要包含“擦边、黄推、福利姬、私房、流血”等可能用于正常讨论的宽泛词。
- 新增或调整词库必须补测试，重点覆盖：
  - 命中。
  - 不误杀正常讨论。
  - `#` 前缀清理。
  - 通配和正则。
  - 排除词优先级。

## MediaShield 规则

- MediaShield 是 X/Twitter 专属隐藏能力，不要写入公开 README 主宣传。
- 默认关闭；开启后也要按群开关或私聊白名单控制。
- 被动检测：
  - 如果 X 解析结果含 `__mediaparser_twitter_possibly_sensitive__`，先只检查政治和暴恐风险；未命中风险则进入打包发送。
  - 如果没有 X 敏感标记，再用 MediaShield 自己的成人被动词库判断。
- 主动检测：
  - 用户消息包含 X 链接，并包含主动关键词时触发。
  - 主动关键词和回应 emoji 可在 WebUI 编辑。
- 发送链路：
  - 生成打码预览卡片。
  - 下载媒体到本地缓存。
  - 生成随机 6 位解压密码。
  - 生成加密 zip。
  - 合并转发只发送预览图和解压密码。
  - zip 使用普通文件上传，不要塞进合并转发 file 节点。
- 文件上传路径要通过 `oneBotUploadFilePath` 映射容器路径和宿主机路径，避免 llbot/OneBot 读不到容器内部路径。

## WebUI 规则

- WebUI 是插件内能力，尽量保持“插件中心 + 插件页面”形态，不做侵入式框架改造。
- WebUI 入口和 API 在 `webui.go`。
- WebUI 必须有 Basic Auth：
  - 支持 `WEBUI_USER` / `WEBUI_PASSWORD` 环境变量。
  - 环境变量优先于旧数据卷认证。
  - 密码保存为加盐哈希。
  - `WEBUI_PASSWORD=off/disabled` 会关闭鉴权，生产禁止使用。
- API 隐私收敛原则：
  - `/api/status` 不返回完整配置、绝对路径、Cookie。
  - 配置接口不返回 Cookie 明文，只返回 `*_cookie_set`。
  - 系统接口不返回服务端敏感绝对路径，除非确有必要。
- 页面布局要紧凑、等高、可一屏扫描；不要做高低起伏的卡片瀑布。
- 表单逻辑要符合用户直觉：
  - 内置词只读预览。
  - 自定义分类可新增、编辑、删除。
  - 全局/平台只负责启用哪些分类。
  - 预览和分类选择尽量合并，减少空白和跳转。
- 改 WebUI 后应本地起服务预览，必要时用浏览器截图检查布局。

## QQBot 与合规边界

- `qqbot_driver.go` 是官方 QQBot 私有驱动相关能力，不一定适合提交到插件 playground。
- 如果目标是合规提交 playground：
  - 可以提交 MediaParser 插件和更合规的 WebUI。
  - 不提交 QQBot 私有驱动，或确保其不侵入整体框架。
  - WebUI 内应检测 QQBot 能力是否可用；不可用时提示官方包无此功能。
- 不要为了 QQBot 私有能力大范围改框架公共代码。

## Docker 与部署

- MediaShield 测试镜像 workflow：`.github/workflows/dockerhub-mediaparser-webui.yml`。
- 当前测试镜像标签：`jaysherlock/zerobot-plugin:shield`。
- workflow 触发：
  - `codex/mediashield` 分支 push。
  - 修改 Dockerfile、docker 目录、Go 入口、`plugin/mediaparser/**` 或 workflow 本身。
- workflow 会：
  - 多架构构建 Go 二进制。
  - 生成 embed reference 文件。
  - 构建并推送 DockerHub 镜像，tag 为 `shield`。
- 部署前必须等待 Actions 成功，避免拉到旧镜像。
- 部署后检查：
  - `docker ps`
  - `docker logs --tail`
  - 镜像 digest。
  - WebUI 是否启动。
  - OneBot / QQBot 连接是否正常。
- 不要把真实 env 文件、部署 IP、账号密码写入仓库。

## 验证流程

- Go 改动后优先运行：

```powershell
gofmt -w plugin/mediaparser/改动文件.go plugin/mediaparser/mediaparser_test.go
go test -count=1 ./plugin/mediaparser
git diff --check
```

- 涉及 WebUI：
  - 本地启动或使用现有容器预览。
  - 检查鉴权。
  - 检查 API 是否泄露路径、Cookie、Token。
  - 检查页面布局是否一屏可读、卡片高度是否整齐。
- 涉及 Docker：
  - 等待 GitHub Actions。
  - 拉取新镜像。
  - 重建容器。
  - 查看启动日志。
- 涉及发送链路：
  - 优先查 MediaParser 日志和 llbot/OneBot 日志。
  - 对大文件上传问题区分压缩耗时、文件上传耗时、平台缓存和 API 超时。
  - 不要重复发送兜底，避免用户收到两套消息。

## 调试原则

- 优先查日志，不凭感觉判断。
- 对结构性问题找唯一事实来源，移除重复逻辑和过期分支。
- 不用宽泛兜底隐藏错误；失败应通过日志和测试清晰暴露。
- 对路径问题，优先确认：
  - 容器内路径。
  - 宿主机挂载路径。
  - OneBot/llbot 实际可读路径。
  - `oneBotUploadFilePath` 映射结果。
- 对 X/Twitter：
  - 区分官方 API、VxTwitter/fx/vx 类非官方接口、yt-dlp 备用路径。
  - 敏感标记只作为 X 平台信号，不应影响其他平台。
  - 无登录状态下可能只能拿到媒体结构或敏感占位，不能假设总能拿正文和媒体。

## README 与公开文档

- README 可以写公开、正常的 Docker 部署方式和基础插件说明。
- README 不主动宣传隐藏能力、敏感插件计划、私有测试账号、内部部署路径。
- 内部项目计划、阶段记录、qb 测试信息等不要提交到 GitHub。

## 常用命令

```powershell
git status --short --branch
git diff --stat
git diff --check
go test -count=1 ./plugin/mediaparser
gh run list --branch codex/mediashield --limit 5
gh run watch <run-id> --exit-status
```

## 最终回复要求

- 简洁说明改了什么、验证了什么、是否提交/推送/部署。
- 如果运行了命令，要把关键结果转述给用户。
- 如果不能验证，要明确说原因和剩余风险。
