# Auth / Tunnel 开发交接

更新日期：2026-09-06。本文是当前进度和剩余工作的唯一交接记录，不是新需求或部署授权。

## 接手入口

先检查三个仓库的 `git status` 和 `git log`，再阅读本文及[已确认规范](superpowers/specs/2026-09-05-nodelane-auth-tunnel-console-design.md)中与任务相关的条款。不要从已删除的阶段计划、旧聊天摘要或历史工作分支重新启动整个开发流程。

| 仓库 | 已验证的功能基准 | 当前边界 |
| --- | --- | --- |
| `nodelane-tunneld` | 本地 `main`：`c876e4e`，主要功能提交 `172038e` | 新控制面和控制台已接入；仍有下述缺口，未发布或部署 |
| `auth` | 本地 `main`：`a22e375`，实现提交 `0a7934e` | Logto 部署包、离线校验和操作手册完成；不是已配置好的真实身份租户 |
| `nodelane-www` | 本地 `main`：`4c2f640` | 静态主站已增加保留语言的控制台入口，不读取 Session |

上述提交是功能证据基准；本次文档清理及后续提交可能使 HEAD 前进。合入本地 `main`、推送 Git、发布镜像/客户端、部署和公网验收是不同状态。

## 优先处理的缺口

1. **ANON-05 尚未完成。** 规范仍要求匿名当前运行统计在本地 `nt` 展示，但 [CLI 输出](../cmd/nt/commands_dependencies.go)和 [Runner 状态](../internal/runclient/runner.go)只有运行标识、地址、到期时间及生命周期状态，没有上传/下载或连接统计。注册路由的流量测试不能代替此项。继续实现或取消此要求须明确处理；若需要改变接口、统计来源或原生复用边界，先与用户确认，不能恢复旧监控/日志系统来绕过约束。
2. **AC-UI-01 尚未完成截图目视验收。** 已检查真实浏览器的尺寸、12 语言、RTL、复制、语言切换及 Escape 焦点恢复，但上一轮执行环境无法读取图片。不能把几何检查或旧截图写成像素级验收通过。
3. **真实身份联调未执行。** 需要用户指定独立测试环境并授权配置 Logto、Resend、Google、Web/Native 应用及第二个 SSO 验证应用；邮箱验证码、主动绑定、Device Flow、Refresh Token 撤销和真实退出需逐项留证。入口见 [auth 操作手册](../../auth/README.md)。
4. **真实部署验收未执行。** DNS/TLS、可信代理、内网管理面、frps 重启/UTC 跨日统计、全新机器上的三种公开安装命令及生产停止传播仍须按规范验收。编译通过不等于 ARM64 实机通过。
5. **版本发布未执行。** `client-version.txt` 仍为 `0.4.4`；本次破坏性 CLI/API 必须确定并发布新客户端版本，不能覆盖旧版本字节。未经授权不推送、发布镜像、轮换密钥、修改提供方或删除真实数据库。

不要将剩余工作概括成“仅差上线配置”，也不要因全部现有测试通过而宣称全部需求闭环。

## 已接入的实现

| 范围 | 当前实现与代码入口 |
| --- | --- |
| 组合与 API | [controlserver](../internal/controlserver)、[controlapi](../internal/controlapi)、[bff](../internal/bff)：配置预检、受保护页面、Web Session/OIDC、归属/权限/CSRF、路由和启动码 API |
| 注册权威存储 | [store](../internal/store)、[domain](../internal/domain)、[routes](../internal/routes)：最终第一版 PostgreSQL Schema、事务配额、域名保留、单运行、一次性兑换及加密幂等响应 |
| 匿名与回收 | [anonymous](../internal/anonymous)、[anonymousapi](../internal/anonymousapi)、[recovery.go](../internal/controlserver/recovery.go)：Redis 原子分配、期限、fence、注册/匿名分别回收，共用原生证据分类 |
| 原生授权 | [frpplugin](../internal/frpplugin)、[frpauth](../internal/frpauth)、[frpanonymous](../internal/frpanonymous)、[frpevidence](../internal/frpevidence)：每次 Login 独立原生会话、逐运行 proof、NewProxy 原子授权及客户端/入口观察 |
| CLI | [cmd/nt](../cmd/nt)、[cliauth](../internal/cliauth)、[runclient](../internal/runclient)、[runsecret](../internal/runsecret)：显式匿名/账户/启动码命令、账户凭据隔离、本地预检、内存凭据输入和停止流程；不包含上述 ANON-05 统计显示 |
| 统计和页面 | [runtimestats](../internal/runtimestats)、[console](../web/src/console)：账号路由原生当前统计；列表、创建、详情、停止、删除、恢复、每次复制生成新启动码、错误及失效处理 |
| 部署交付代码 | [deploy](../deploy)、[安装器](../internal/server/assets)：完整原版 frps 配置、host-network Compose、显式资源初始化与三种 Shell 命令；未进行公网发布 |

旧 Server/Admin/API、旧匿名凭据/JWT/存储/lease、CLI monitor 和旧日志实现已移除。`internal/client/embedded_frp.go` 是仍在使用的原版 frpc 封装，不因目录历史名称而删除。

## 不得误改的边界

- 原版 frps/frpc 固定 `0.70.0`，Logto 固定 `1.43.0`；不维护补丁、不新增数据面或自建身份协议。
- MIG-01 是破坏性版本：无旧 API、导入、双写或数据库升级兼容；新数据库初始化和兼容性检查已实现，不能添加自动删库或重启清空资源。
- 永久路由仅 HTTP、每账号 5 条、每路由 1 个活动运行；删除后名称保留 7 天且必须完成旧入口释放。HTTP 访问并发限制本轮不做。
- 只有当前原生连接数和今日 UTC 字节，不建设历史图、请求日志、Prometheus、账单或字节配额。本地服务视角：上传对应 `todayTrafficOut`，下载对应 `todayTrafficIn`。
- 回收标准是安全复用入口，不是证明所有旧数据流结束。未批准 NewProxy 的终止运行走权威存储 CAS；批准过的运行需要先确认原生客户端已断开，再核对入口；证据不足继续占用，不能仅凭 404/超时释放。
- `nt launch` 不接触账户凭据；运行凭据使用 Linux memfd / Windows 同用户同进程命名管道。frps 原生共享 token 为空、强制 TLS；非空 `NT_FRP_PROXY_URL` 在分配前拒绝。
- Web 国际化只有 [i18n/index.ts](../web/src/i18n/index.ts) 的 `getTranslation` 入口，文案在原有 12 个语言包的 `console` / `errors` 中，不重新创建控制台词典。
- Tunnel 网站和控制台共用 [HeaderFrame](../web/src/components/HeaderFrame.astro)、[LanguageSelect](../web/src/components/LanguageSelect.astro) 和 [design-system.css](../web/src/styles/design-system.css)。主站另有本仓库 CSS；品牌关系一致不表示跨仓库 CSS 字节相同。旧设计尺寸和生成提示不能覆盖源码。

## 验证证据与复现

2026-09-06 功能基准上的验证记录，不应被当作后续修改的自动通过证明：

- Tunnel：真实隔离 PostgreSQL/Redis 下 `go test ./... -count=1 -timeout 180s` 和 `go vet ./...` 通过。
- Web：`pnpm test` 28 通过、1 项 Linux 专属测试在 Windows 跳过；`test:built` 20 通过；`test:embedded` 1 通过。Go 嵌入的 36 个文件与 Astro 构建逐字节一致，并验证 Windows LF 规则不影响 PNG。
- 原生端到端：[注册运行](../internal/controlserver/end_to_end_test.go)、[匿名 HTTP/TCP/UDP](../internal/controlserver/anonymous_end_to_end_test.go)、[流量方向](../internal/controlserver/traffic_end_to_end_test.go)通过。已证明入口复用、迟到旧 stop 不影响新运行、匿名流程不写账号表，以及字节在连接关闭后更新。
- 编译：`nt` 的 Linux/Windows × amd64/arm64，以及 `tunneld` 的 Linux/Windows amd64 均通过；不代表 ARM64 实机或公开安装命令验收。
- auth：80 项离线测试通过；主站：类型检查、12 页构建及 2 项 CSS 测试通过。
- 独立审查覆盖存储/回收、原生会话、控制台、统一国际化、共享组件和交付清理；不能据此覆盖前述未完成项。

| 工作目录 | 验证命令 |
| --- | --- |
| `auth` | `npm test`；真实配置预检按其 README，不能虚构输入 |
| `nodelane-tunneld` | `go test ./... -count=1 -timeout 180s`、`go vet ./...`、`go build ./cmd/tunneld ./cmd/nt` |
| `nodelane-tunneld/web` | `pnpm install --frozen-lockfile`、`pnpm test`、`pnpm test:built`，同步产物后 `pnpm test:embedded` |
| `nodelane-www` | `pnpm build`、`node --test tests/css-scale-contract.test.mjs` |

Go 集成测试必须显式设置 `NODELANE_TEST_DATABASE_URL` 和 `NODELANE_TEST_REDIS_URL`。未设置时相关测试会跳过，不能称为真实数据库集成通过。[夹具守卫](../internal/controlserver/fixture_test.go)要求回环测试连接、数据库 `nodelane_control_test` 和 `control_fixture_v1` 标记，以及 Redis DB 15 中的 `bff_fixture_v1` 标记。连接信息以现场为准，不使用生产环境变量或绕过守卫；共享测试容器不属于可随意删除的临时输出。

前端修改必须构建并同步 `web/dist` 到 `internal/server/assets/web`，只对确认过的生成目录操作；不能手改嵌入文件或镜像整个仓库。[Web 开发说明](../web/README.md)是操作入口。

旧 localhost 预览和 PID 不作为交接依赖。需要浏览器夹具时，在上述隔离测试环境设置 `NODELANE_CONSOLE_PREVIEW=1`，运行 `go test ./internal/controlserver -run '^TestConsoleBrowserFixture$' -count=1 -v -timeout 35m`；使用当次输出的 URL。它提供合成 Session、约 30 分钟后清理，仅用于本地测试，不证明真实登录可用。

## 后续更新方式

只更新本文中的进度、证据和剩余项；已确认需求变更另经用户确认后更新规范。不要再生成重复的阶段完成报告或强制后续 AI 从头编写计划。旧分支可能通过 cherry-pick 整合，不能因不是图祖先就再次合并或恢复旧实现。
