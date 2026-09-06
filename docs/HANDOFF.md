# Auth / Tunnel 开发交接

更新日期：2026-09-06。本文是当前进度和剩余工作的唯一交接记录，不是新需求或部署授权。

## 接手入口

先检查三个仓库的 `git status` 和 `git log`，再阅读本文及[已确认规范](superpowers/specs/2026-09-05-nodelane-auth-tunnel-console-design.md)中与任务相关的条款。不要从已删除的阶段计划、旧聊天摘要或历史工作分支重新启动整个开发流程。

| 仓库 | 已验证的功能基准 | 当前边界 |
| --- | --- | --- |
| `nodelane-tunneld` | 功能基准 `c876e4e`，主要功能提交 `172038e`；发布源码 `1a64c38` | `0.5.0` 镜像和 `stable` 已发布；生产未部署，仍有下述缺口 |
| `auth` | 原功能基准 `a22e375`；发布源码 `18c92f9` | 统一主题及邮箱验证入口修复本地已应用；根路径及协议链接有新的未提交修改；已发布 `.1` 镜像不含这些新修改，生产未部署 |
| `nodelane-www` | 功能基准 `4c2f640`；当前 `main`：`813e84e` | 控制台入口不读取 Session；12 语言隐私政策和服务条款有新的未提交修改，未部署 |

上述提交是功能证据基准；本次文档清理及后续提交可能使 HEAD 前进。合入本地 `main`、推送 Git、发布镜像/客户端、部署和公网验收是不同状态。

## 优先处理的缺口与发布边界

1. **ANON-05 尚未完成。** 规范仍要求匿名当前运行统计在本地 `nt` 展示，但 [CLI 输出](../cmd/nt/commands_dependencies.go)和 [Runner 状态](../internal/runclient/runner.go)只有运行标识、地址、到期时间及生命周期状态，没有上传/下载或连接统计。注册路由的流量测试不能代替此项。继续实现或取消此要求须明确处理；若需要改变接口、统计来源或原生复用边界，先与用户确认，不能恢复旧监控/日志系统来绕过约束。
2. **AC-UI-01 尚未完成截图目视验收。** 已检查真实浏览器的尺寸、12 语言、RTL、复制、语言切换及 Escape 焦点恢复，但上一轮执行环境无法读取图片。不能把几何检查或旧截图写成像素级验收通过。
3. **真实身份联调仅部分完成。** 真实 Google 登录、第二应用 SSO、邮箱验证码注册，以及 Linux 容器内 CLI Device Flow → 跨进程刷新 → 路由运行 → 退出撤销已通过。已有邮箱账号的独立再次登录、错误/过期/重用验证码、Google 主动绑定/冲突隔离、Access Token 真实到期拒绝和浏览器真实退出仍待验收；不把 CLI 退出当作浏览器退出。详见下节和 [auth 操作手册](../../auth/README.md)。
4. **真实部署验收未执行。** DNS/TLS、可信代理、内网管理面、frps 重启/UTC 跨日统计、全新机器上的三种公开安装命令及生产停止传播仍须按规范验收。编译通过不等于 ARM64 实机通过。
5. **0.5.0 Git 与镜像发布已完成。** 2026-09-06 用户授权的 Tunnel Git 推送、版本镜像和 `stable` 发布均已回读确认；控制面及内置 `nt` 均为 `0.5.0`，不覆盖旧 `0.4.4` 客户端字节。没有额外触发 `nt-v*` GitHub 客户端 Release，也没有执行生产部署、密钥轮换、提供方修改或真实数据库清理。详细证据与新部署要求见下一节。

不要将剩余工作概括成“仅差上线配置”，也不要因全部现有测试通过而宣称全部需求闭环。

## Auth 页面及 OAuth 公开页面补充

2026-09-06 用户要求并行处理认证页面规范化、直接访问 Auth 根路径的 404、社交绑定冲突提示，以及 Google OAuth 所需公开页面；随后明确 Logto 标志若难以去除可以保留。本节是上述请求的本地结果，不增加生产或上游补丁授权。

- **根路径已在本地修复。** 仅原生 `unknownSessionRedirectUrl` 不足以覆盖已有 interaction 的裸 `/`，因为原生守卫会放行，而 SPA 没有 `/` 页面。现在 [local.nginx.conf](../../auth/deploy/local.nginx.conf) 仅精确匹配 `/`，固定 302 到同源 `/account`；[branding.mjs](../../auth/scripts/branding.mjs) 同时配置原生会话缺失恢复，用于过期的命名登录入口。已有 OAuth、社交回调和账户路由不变，不转发来源查询参数。已运行本地 `nginx -t`、reload、无 Cookie HTTPS 检查及已有真实登录会话的浏览器检查；旧浏览器缓存经强制刷新后，裸根路径进入 `/account/security`。生产只提供 [既有 TLS server 的配置片段](../../auth/deploy/auth-root.nginx.conf.example)，未应用。
- **主题和协议入口已在本地应用。** 登录、注册与账户页使用现有 NodeLane 主题和原生协议链接，账户页页脚补齐窄屏及 RTL 换行。固定自托管版本拒绝 `hideLogtoBranding=true`，按用户最新范围保留原生标志，不规避其前端保护、不修改上游源码。品牌工具只更改协议链接、同源恢复地址和受管理 CSS，保留原同意策略、身份方法及无自动合并约束；应用后 `--check` 返回无漂移。
- **Google 公开页面已实现但未部署。** [主站](../../nodelane-www/README.md#oauth-public-pages) 新增原有 12 语言的隐私政策、服务条款，共 36 个静态页面；页脚互链、保留当前文档的语言切换、canonical、hreflang 和 sitemap 已覆盖。正式填写 `https://www.nodelane.net/`、`https://www.nodelane.net/privacy/`、`https://www.nodelane.net/terms/`，授权顶级私有域为 `nodelane.net`。正式网站部署、公开可达性、域名所有权验证及 Google 控制台保存均未执行。公开讨论区只用于发起不含个人信息的联系；发布前仍建议确认实际可用的私密隐私/删除联系渠道并审核运营政策，不虚构邮箱、公司主体或删除时限。
- **社交绑定冲突提示仍未修复，需确认上游边界。** 固定原生 Account Center 的标准 Add 流程先保存返回 URL；冲突响应进入 `finalizeSocialFlowFailure` 后先设置内存 toast，再由 `window.location.assign` 整页跳转，导致提示销毁。没有可靠原生配置或 CSS 修复；无旧返回状态的直接任务链接不能修复标准流程。新增 [上游诊断测试](../../auth/tests/account-feedback-upstream.test.mjs)，核对实际源码及构建 source map 后在内存中执行原函数，5 项通过仅证明缺陷，不代表提示验收通过。可靠修复需要用户明确允许一个仅处理失败后导航/错误展示的前端补丁，或另行批准上游版本变更；不得通过合并账号、转移归属或取消验证绕过。此次没有发送验证码或执行真实绑定。
- **验证与边界。** Auth 最新全套 328 项：323 通过、5 项显式跳过（上游诊断在宿主机默认跳过、POSIX 权限及 3 项 Windows 符号链接条件）；固定运行镜像的原生根路径/提示构造函数已显式执行，上游社交诊断另在 Docker 离线执行 5 项通过。主站构建 0 诊断、36 页；源码测试 4 项和构建测试 26 项全通过。真实本地 TLS/Discovery/JWKS、Native 配置和 Tunnel PKCE 登录跳转仍通过。浏览器检查了登录、注册、账户页及新增法律页面的中文/阿拉伯语、320/390 窄屏、桌面、图片、文档切换和页脚位置；截图已生成，但当前模型无法读取图像，仍不关闭截图目视验收。首页原有 320px 大标题溢出不属于新增法律页/页脚，原首页样式未改。没有提交、推送、发布新镜像或部署生产；此前 `.1` 镜像不包含本节新源码和主题。

## 0.5.1 TCP 转发端口补丁

2026-09-06 用户确认继续采用两个独立容器，并要求保留 `frp.nodelane.net:7001` 经原始 TCP 代理转发到 frps 内部 `7000`。此前建议统一端口不适用于该实际部署。用户授权本地生成部署配置与 FRP 证书，并明确要求对不支持的端口映射发布 `0.5.1` 补丁；未授权修改生产容器、代理、数据库或身份提供方。

- 控制面新增可选 `FRPS_BIND_PORT`，未设置或留空时沿用 `FRP_SERVER_PORT`；前者用于核对原版 frps 的内部监听，后者继续作为客户端公开端口。模板与 Compose 传入同样的两项设置，TLS、SNI、回环插件和私有管理面校验保持不变。
- 该部署使用 `FRP_SERVER_PORT=7001`、`FRPS_BIND_PORT=7000`，代理必须原样转发 TCP/TLS，不能改成 HTTP 代理或在代理处终止 FRP TLS。网站、控制台及 HTTP 隧道入口不受这个控制连接端口拆分影响。
- 仅服务端补丁版本升为 `0.5.1`，`client-version.txt` 保持 `0.5.0`；发布前后必须核对四个平台已发布客户端包的原始字节和 SHA-256，不重新覆盖同名客户端版本。
- 本地配置、七个新控制面密钥、新管理凭据和 FRP 服务端证书保存在仓库外的当前用户专属目录；CA 私钥只在独立离线目录，不进入上传包。沿用既有 Redis 连接但更换命名空间，新 PostgreSQL 角色/数据库仅生成一次性人工执行 SQL；正式 Logto Web/Native 绑定保持明确待填，不使用本地测试身份参数。

Git、镜像及本轮验证结果在发布完成后补充；下节保留 `0.5.0` 的发布证据和通用部署步骤，不代表 `0.5.1` 已经上线。

## 0.5.0 发布与部署准备

2026-09-06 用户授权本仓库 Git 和镜像发布；发布源码提交 `1a64c38` 已推送并回读确认，后续本节记录是文档提交。镜像从该源码提交的纯 Git 快照构建，排除工作目录中的旧工作树、未跟踪 `nt` 二进制和私密输入。以下本地联调章节保留各阶段发生时的状态，不是当前发布状态。

- Registry：`docker.nodelane.net/nodelane/tunneld:0.5.0` 与 `:stable` 均已回读为 `sha256:37cc92e5486f17b31852087822070d447df0ab911f3d0f64024f567703d31521`，保留 `linux/amd64`、`linux/arm64` 及两份构建证明。上传期间的 `522` / `524` 经同一份镜像按架构补传恢复，没有重建或更换客户端包字节。
- 发布检查：真实受守卫保护的 PostgreSQL/Redis 下 Go 全套测试、`go vet ./...`、双程序构建通过；Windows 测试有两项预期辅助/预览跳过，Linux 专属测试不在本次宿主机套件内。Web 源测试 29 通过、1 项 POSIX 跳过，构建测试 22 通过，嵌入字节一致性 1 通过，部署配置回归通过。
- 拉取后隔离验证：正常配置、原版 frps 和全新临时 PostgreSQL/Redis 完成匿名初始化及正常启动；`/healthz`、`/api/v1/client-config`、旧/私有路径拒绝、24 个公开前端文件逐字节匹配均通过。`/releases/stable.txt` 为 `0.5.0`，`/run.sh` 为 5530 字节且 `CR=0`。
- 四个平台客户端包的 SHA-256、原版 frp 许可证和目标架构均通过；Windows/amd64、Linux/amd64 及模拟 Linux/arm64 的 `nt --version` 为 `0.5.0`，两个 Linux 架构的服务端版本也一致。Windows/arm64 只验证包及构建目标，不冒充 ARM64 实机验收。所有本次临时容器、网络、证书和密钥已清理，没有消费真实身份凭据。

相对旧版，这不是只替换镜像的升级。正式部署前必须准备：

- 新的 Tunnel PostgreSQL 数据库与 `DATABASE_URL`，不能复用旧版业务数据库；首次正常启动自动初始化最终第一版 Schema，不提供旧数据导入或自动清库。
- 新的 `REDIS_PREFIX` 及实际 Redis 连接。Redis 必须为无副本的 standalone master，`maxmemory-policy=noeviction`，服务账号允许 Lua、`INFO`、`SCAN`、`CONFIG GET` 等现有操作；换前缀不能绕过实例约束。
- [.env.example](../.env.example) 中的七个互异密钥：`CONTROL_LAUNCH_PEPPER`、`CONTROL_RUN_PEPPER`、`CONTROL_REPLAY_KEY`、`SESSION_ENCRYPTION_KEY`、`ANONYMOUS_CREDENTIAL_PEPPER`、`ANONYMOUS_REPLAY_KEY`、`ANONYMOUS_FENCE_OWNER_TOKEN`；每个是 32 字节密码学随机值，使用 base64 或无填充 base64url，私密保存并保持稳定。
- 正式 `PUBLIC_ORIGIN`、`OIDC_ISSUER`、`OIDC_WEB_CLIENT_ID`、`OIDC_WEB_CLIENT_SECRET`、`OIDC_NATIVE_CLIENT_ID`。Web 与 Native 必须为独立真实应用；Web 回调为 `https://tunnel.nodelane.net/auth/callback`，退出回调为 `https://tunnel.nodelane.net/`。Native 启用 Device Flow；API Resource 固定为 `https://tunnel.nodelane.net/api`，默认角色授予 `routes:read` / `runs:start`。Logto、邮箱验证码、Google、Account Center 和认证日志策略按 [auth 手册](../../auth/README.md)独立完成，不能复用本地测试凭据冒充生产配置。
- 原版 frps `0.70.0` 及本次完整 [frps.toml](../deploy/frps.toml)，同步配置管理账号、强制 TLS、CA/证书/私钥路径及全部六个插件回调。模板与公有证书须在相同绝对路径只读挂载且非 root Tunnel 可读；私钥只挂 frps。移除非空 `FRP_AUTH_TOKEN`，不再使用 `DEV_MODE`、`PUBLIC_SCHEME`、`TOKEN_PEPPER`、`TUNNEL_JWT_SECRET` 或 `ADMIN_TOKEN` 作为新控制面配置。
- Linux host network 与正式 DNS/TLS。控制站 HTTPS 转发到 `127.0.0.1:9000`；通配 HTTP 隧道转发到 `8080` 并保留 Host，7000 为原始 TCP。9001 插件、7500 管理面、8080 原生 HTTP 入口及数据库/Redis 不直接公开；代理覆盖 `X-Real-IP` 并匹配 `TRUSTED_PROXY_CIDRS`。旧 CLI/API 不兼容，部署时同步更新客户端。

首次部署命令说明，**本次未在生产执行**。私密环境文件须先将 `TUNNELD_IMAGE` 设为 `docker.nodelane.net/nodelane/tunneld:0.5.0` 并填齐上述真实配置；正式 Logto 必须另行就绪。维护窗口中确认旧客户端已停止、旧 frps 已退出、新数据面及新匿名命名空间干净后执行：

```sh
dc() {
  docker compose --env-file /etc/nodelane/tunneld.env \
    -f deploy/compose.yaml -f deploy/compose.registry.yaml "$@"
}
dc config --quiet
dc pull
dc up -d frps
dc run --rm --no-deps tunneld anonymous-resources init --confirm-clean-data-plane
dc up -d tunneld
```

匿名初始化只针对确认过的全新命名空间，重复执行会拒绝；不是清理或重启命令。最后一步首次初始化 PostgreSQL。生产切换、真实身份/公开安装命令、ARM64 实机以及本节上方的未完验收仍须单独完成。

## Logto 品牌与邮箱绑定修复

2026-09-06 用户授权统一 Logto 登录页及 Account Center 风格、修复邮箱登录后的 Google 绑定入口，并提交 Auth / 发布配置镜像。Tunnel `0.5.0` 由并行发布任务处理，上节是它的独立结果；本节不覆盖生产、Google 控制台或用户身份变更。

- **Git 与镜像。** Auth 源码提交 `18c92f9b5731192fa6627f3c81d0e38515f4ce80` 已本地提交；该仓库没有 remote，未虚构远端推送。`docker.nodelane.net/nodelane/auth:1.43.0-nodelane.1` 已发布并分别通过 Docker / regctl 回读为 `sha256:030c0cc95b31df8e602d7bebbc65203508305758b249f71cae669947104987a4`，包含 `linux/amd64`、`linux/arm64` 及两份构建证明；未发布 Auth `stable` 别名。普通推送被约 264 MB 原版层触发 `413`，改用固定 regctl 0.11.6 的 16 MiB 原生分块上传，同一 OCI Layout 的摘要完全不变，没有重建或改生产网关。
- **发布验证。** 按上述摘要拉取后，amd64 与模拟 arm64 执行内置工具通过，Logto 均为 `1.43.0`，主题 SHA-256 为 `07a2c5e39b23359f6cd469f95cb1add6975b771a1382a3da7cbcb3eb3c814a2e`，CSS 无 CR；没有实际 ARM64 主机验收。原版层及运行配置逐项相同，仅增加两个 COPY 层。最终 auth 全套 295 项：291 通过、4 项 Windows/平台条件跳过；Go 密钥生成器测试通过。48 项品牌工具测试在 Windows 与隔离 Linux 各 47 通过、1 项异平台权限测试跳过。真实本地 TLS/Discovery/JWKS、Native 公共配置、BFF 登录跳转及主题无漂移检查通过；41 个 Git 候选文件仅含代码/资源，发布前私密值扫描无命中。
- **根因和最小修复。** 原配置只有 `fields.social="Edit"` / `fields.password="Off"`，缺省 `fields.email` 等于 `Off`。原版 Logto 1.43.0 的 `/api/my-account` 隐藏 `primaryEmail`，但仍报告存在安全验证方式，导致 Account Center 列出空验证列表。现补 `fields.email="ReadOnly"`；已有批准的 `Edit` 保留。密码、自动关联仍关闭，绑定前二次验证不变，不合并既有账号。
- **原生品牌扩展。** [auth/branding/nodelane.css](../../auth/branding/nodelane.css) 复用官网设计令牌和现有 PNG，经登录体验、Account Center 两处 `customCss` 注入；原版账号页面、协议、连接器和语言行为不复制、不打补丁。主题覆盖按钮、输入框、错误/禁用状态、账户行与菜单、窄窗口及 RTL，并解决原版邮箱行在窄桌面窗口中仅剩极窄文本列的问题。头像、Google 标识仍使用原生资产。
- **配置与交付。** [apply-branding.mjs](../../auth/scripts/apply-branding.mjs) 默认只读计划，`--apply` 才 PATCH，`--check` 对漂移返回失败；外来 CSS 需显式替换参数，写前复查并写后回读，错误只记录端点名和固定诊断码。生产 issuer 精确绑定 `https://auth.nodelane.net/oidc`，本地显式 profile 固定 TLS + 回环解析。Windows ACL/POSIX 私密输入校验保留，凭据不在参数或输出中。镜像仅在原版固定层上 COPY `/opt/nodelane` 工具与 CSS，默认启动、数据库和租户不变。
- **真实本地结果。** 现有本地租户已应用并回读无漂移。Chrome 的邮箱账号点击 Google「添加」后进入「验证您的邮箱」并显示发送按钮，原错误已消失；没有发送本次验证码，也没有执行 Google 最终绑定。固定原版函数的离线回归确认邮箱可读后仍要求已验证身份。邮箱验证码注册、CLI 撤销等既有结果不受影响。
- **浏览器边界。** 已检查中文桌面、390/320 窄屏及阿拉伯语 RTL 登录，确认图片加载、固定按钮尺寸、主题颜色和无横向溢出；账户窄屏邮箱行及菜单可用。登录截图存于本任务可视化目录；包含账户信息的截图只存本地私密身份目录。当前模型仍无法读取截图图像，不能把这些结果称为目视像素验收或关闭 AC-UI-01。验证码发送/错误/到期/复用、Google 最终绑定及冲突拒绝仍待真人验收。

上线必须使用正式 Logto 独立数据库、私有 Admin HTTPS、正确 Core TLS/反代、生产连接器与 Web/Native 应用。主题与 Account 设置属于数据库配置，单独拉镜像不会自动带入；[auth README 第 15 节](../../auth/README.md#15-registry-deployment)提供 digest 固定镜像、原生初始化、M2M 一次性主题应用和回读命令，不复用本地测试凭据。

## 本轮身份联调准备

2026-09-06，先在上述本地基准之后完成兼容修复；该阶段没有创建真实身份租户、提供方应用或密钥，也没有读取或变更生产数据库。随后用户另行授权本地 Docker，执行结果见“本地 Docker 身份环境”。代码仍未提交、推送或发布，不能把本地 Docker 运行称为生产部署。

- **Logto 授权码回调兼容。** 固定版本的原生授权响应包含 `iss`，旧 BFF 仅允许 `code/state` 导致正常回调返回 `401`。现允许唯一、非空的 `iss` 并与已配置/发现的 issuer 精确比较；Discovery 声明支持该参数时缺失也拒绝，且在消费登录事务或兑换授权码前检查。覆盖正确、缺失、重复、异源、非精确值和重放。
- **默认签名算法兼容。** 固定 Logto 新初始化实例使用 EC P-384 / ES384；消费者及 auth Discovery 校验现支持 RS256 和 ES384，不接受 HS256、ES256 或 `none`，不改 Logto 密钥。覆盖 Web Exchange/Refresh、Native Token、JWKS 轮换和签名/算法篡改；原有 issuer、audience、nonce、Scope、时间和 Token 类型校验保留。
- **Web 离线授权兼容。** 真实 Google 联调发现缺少 `prompt=consent` 时固定 Logto 会从授权中删除 `offline_access`，Token Endpoint 虽返回 `200` 却不发 Refresh Token，BFF 因此拒绝会话。现授权 URL 明确带 `prompt=consent`，不添加 `login` 或 `max_age`，保持 SSO；新增回归先红后绿，并在真实 Google 会话下验证成功。
- **Device Flow 本地体验。** `nt login` 展示到期时间，并优先尝试打开已验证的完整验证地址；打开浏览器最长 3 秒、可取消、不经 shell，失败仍可手动授权。Windows 隐藏启动器，Linux 无桌面时跳过；新增测试不代表 Linux 桌面或 ARM64 实机验收。
- **退出失败状态。** 远端撤销失败但本地 Session 已失效时，控制台保留明确的本地已退出提示、隐藏业务视图并停止轮询，不自动进入仍有效的 Logto SSO。会话结果未知时可重试；确认仍登录才恢复轮询。
- **操作手册。** auth README 已补上固定版本的原生字段，包括 `customClientMetadata.isDeviceFlow=true`、默认 User Role、Account Center 的 `fields.social="Edit"` / `fields.password="Off"`；意图 JSON 不能直接当作 Logto Management API 请求体。

本轮已验证：

- 现场确认回环 PostgreSQL 的 `nodelane_control_test` / `control_fixture_v1` 与 Redis DB 15 的 `bff_fixture_v1` 标记后，`go test ./... -count=1 -timeout 180s`、`go vet ./...` 和 Go 构建通过。新增 [组合回调测试](../internal/controlserver/oidc_callback_test.go)真实使用 PostgreSQL/Redis，但 OIDC 提供方仍为合成 TLS 测试服务。
- `nt` 的 Linux/Windows × amd64/arm64 编译通过；`tunneld` 的 Linux/Windows amd64 编译通过。Windows 上的启动器参数与受控子进程超时回收测试通过；Linux 专属浏览器测试未执行，两平台都未验收系统启动器实际打开浏览器。
- auth `npm test`：82 项通过。Web `pnpm test`：28 通过、1 项 Linux 专属跳过；`test:built`：22 通过；同步生成目录后 `test:embedded`：1 通过。
- 真实本地浏览器点击退出后，DOM 显示“已退出本地会话，身份服务退出尚未确认”，并停留原地址；截图工具返回的图片仍无法目视读取，因此 **AC-UI-01 不得标记通过**。预览已关闭并清理其专属测试数据，共享测试容器保留。
- [浏览器夹具](../internal/controlserver/console_preview_test.go)现使用纯回环的失败身份服务，避免合成 Session 的退出操作接触默认生产身份地址。它仍不证明真实 Logto、邮件或 Google 可用。

密钥保存在仓库之外的私密文件或部署密钥存储中，仅传递位置，不进入 Git、截图或验收报告。生产部署包及默认校验继续绑定生产域名；本地使用显式 `local-docker` 合约及独立 Compose 文件，不关闭精确 issuer 校验。

## 本地 Docker 身份环境

2026-09-06 用户明确授权搭建本地 Docker 环境、生成独立随机密钥，并提供临时 Resend/Google 凭据及测试收件箱；随后确认 Google URI 已保存，并明确授权本次 CA 导入。仅用于本地测试；没有修改 Google 控制台、生产 DNS 或系统 hosts，也没有使用共享测试数据库。

- Compose 项目：`nodelane-identity-local`。8 个服务为 `edge`、`logto`、`logto-db`、`tunnel-db`、`redis`、`frps`、`tunneld`、`sso-check`，仍在运行。全部宿主机发布端口绑定 `127.0.0.1`，管理/插件/数据库端口不直接发布；专属命名卷保留，不自动删卷或重新 seed。
- 运行输入：`auth/deploy/local.compose.yaml` 与 `local.tunnel.compose.yaml`。原版 Logto 固定 `1.43.0` 及原批准 digest；原版 frps 为 `0.70.0`。本地 Tunnel 镜像 `nodelane-tunneld:identity-local` 来自当前源码的静态 Linux/amd64 构建，不是版本发布。
- 私密状态：`C:/Users/Wy/AppData/Local/NodeLane/identity-docker`，父目录 Windows ACL 仅当前用户访问。已生成独立数据库/Redis/Admin/frps 密码、7 个互异控制面密钥和短期 CA/服务证书；CA 私钥不挂入任何容器。第三方凭据、原生 M2M 和应用 Secret 均仅存该目录，不在本文记录值。
- 原生管理员：`nodelane_local_admin`，随机密码在私密 `secrets.json`。经原版 Logto Admin OIDC + Experience API 创建，没有 SQL 写身份表或使用 seed 内部 M2M 秘密。首次驱动曾误解析原生注册的 `201 Created` 文本，在已有管理员上正常登录恢复并完成 M2M；驱动已修复该响应及补回归，尚未为验证首次一步完成而重建数据库。
- 已真实配置并回读：Web `xvg55uq36xkj6ast9qad7`、Native `8mt6v7s4g1pfez5q4wgwe`、SSO `7o8km251m7wb5pvm6bvsv`；Native Device Flow、API Resource `https://tunnel.nodelane.net/api`、两个权限和默认 `tunnel-user` 角色。邮箱验证码启用、密码关闭、Google 启用、自动关联关闭；Account Center 开启社交编辑且关闭密码编辑。
- Google 连接器实例 `local-google` 已在本地保存实际测试凭据；用户确认保存来源 `https://localhost:3443` 和重定向 `https://localhost:3443/callback/local-google`。真实浏览器已进入 Google 的 NodeLane 授权页，用户亲自登录并返回 Logto，证明 Google 配置已生效；原版 `/callback/social/local-google` 是内部继续流程，不需额外注册到 Google。
- 原生 SMTP 测试已成功提交给 Resend；未验证收件人实际收到，也没有把测试邮件的固定测试码当作用户注册/登录成功。临时提供方凭据测试结束后应由用户撤销；旧生产 Key 撤销不由本地结果代替。
- 本轮 `auth npm test`（显式设置 `NODELANE_LOCAL_SSO_RUNTIME`）共 219 项：216 通过、3 项文件符号链接测试因 Windows 权限跳过；Windows 目录 junction 防护和真实已安装 SDK 的离线测试实际通过。Go 密钥/证书生成器测试通过；扫描两仓库 373 个 Git 候选文件未发现本次密钥值。配置脚本重跑仅复验已有匹配配置，未再次发送测试邮件。

| 本地地址 | 已验证的边界 |
| --- | --- |
| `https://localhost:3443` | 真正 Logto；TLS/Discovery/issuer/JWKS 已校验，当前 EC P-384 / ES384 |
| `http://127.0.0.1:3302` | 原生 Admin；本地专属 HTTP 入口、独立 Cookie 主机，不代表生产 Admin HTTPS 验收 |
| `https://tunnel.localhost:3443` | Google 和邮箱各自成功建立 BFF Session；专属数据库当前有 2 个该 issuer 的独立账号投影 |
| `https://localhost:9443/api/v1` | CLI 专用 API 别名，避免本机将 `tunnel.localhost` 解析到非回环地址 |
| `https://sso.localhost:3443` | 重启后新建独立应用会话，真实浏览器无需再次输入凭据，`/result` 显示认证成功；只读数据库摘要比较确认同一 issuer/subject |
| `127.0.0.1:17000` / `127.0.0.1:18080` | frps TLS 控制连接 / HTTP 探测入口；数据面显示地址不带本地 18080，探测必须显式带端口及正确 Host |

最初的 CLI 探测仅获取设备码后停止；后续完整授权与撤销证据见“CLI 真实验收闭环”。宿主机 `NT_CA_FILE` 不会配置 API/OIDC 信任；本次使用专属 Linux CLI 容器内的 `SSL_CERT_FILE`，未触碰用户现有登录状态。

已在用户明确授权后执行 `auth/scripts/trust-local-ca.ps1`，将短期测试 CA 导入 `Cert:\CurrentUser\Root`，未修改机器级证书库。证书指纹 `67CE969635B71F60C6ED1E2215B041A5A4CB19F8`；Windows 默认 HTTPS 校验及内置浏览器均已验证，不使用跳过证书错误的选项。

本次回调故障定位和闭环：

- 用户亲自完成 Google 登录后，原版 Logto 已验证社交身份并创建用户，但缺少 consent prompt 导致 BFF 没有得到 Refresh Token；在内置浏览器中表现为空白回调页。补上标准参数并重建本地 Tunnel 容器后，复用同一个 Google/Logto 会话直接进入 `/console/tunnels?lang=zh-CN`，没有再次要求凭据；原来停住的标签页已切回控制台。
- 第二 SSO 测试应用的 JSON 响应在内置浏览器中被阻止显示。现仅对浏览器 `Accept: text/html` 提供转义的只读结果 HTML，JSON 接口及字段白名单保留，CSP 禁止脚本。重启其纯内存会话后重新授权，真实结果为 `authenticated=true`，新应用 Client ID 与 Web Client ID 不同；PostgreSQL 只读摘要比较仅输出 `true`，未展示真实 subject/email。
- 原版 Logto 无可用的请求日志关闭环境开关，且默认日志包含回调查询参数。本地 `logto` 服务已采用 Docker `logging.driver: none` 并重建，旧容器及其 Docker 日志不再保留，数据库/账号/应用不变；这不是应用审计库或浏览器历史的清理，也不冒充生产日志策略验收。
- 新改动之后，真实隔离 PostgreSQL/Redis 下 `go test ./... -count=1 -timeout 180s` 和 `go vet ./...` 通过；本地 Linux/amd64 镜像已重建且 `check-local.mjs` 实际验证 `prompt=consent`。SSO HTML 输出新增回归后，auth 全套 220 项：217 通过、3 项 Windows 文件符号链接测试跳过；不据此宣称未执行的身份用例通过。

邮箱注册及 CLI 授权结果见下方。接续仍需用户完成再次登录的验证码、浏览器真实退出及主动绑定步骤；已有 Google 身份已被本次 Google 用户占用，不能用“合并既有账号”绕过主动绑定验收，绑定用例需未占用的 Google 身份。`auth/README.md` 的本地 Docker 节包含操作入口。

邮箱注册验收补充（2026-09-06）：

- 用户即时授权向约定测试邮箱发送本次验证码；Chrome 使用独立于内置浏览器 Google 会话的原生邮箱注册流程。页面随后显示该测试邮箱并进入 Tunnel 控制台，未读取或保存用户输入的验证码。
- 仅对本次精确邮箱用户、Web Client ID 和注册时间窗口查询原生 `Interaction.Register.Submit` 审计；白名单证据为 `result=Success` 且 `verificationRecords` 中 `type=EmailVerificationCode, verified=true`。事件时间为 `1788694311126`（UTC 毫秒）。不输出日志中验证码、IP、User-Agent、完整 payload 或身份 subject。
- 实例仍为 `signUp.verify=true`、密码关闭；精确邮箱用户只有 1 个且 `identities` 为空。按该用户的真实 `issuer + subject` 在专属 Tunnel 数据库只读核对投影唯一，两个账号未合并。
- 此次证明邮箱验证码注册及注册后的会话成功，不替代已有邮箱用户的再次登录、验证码错误/过期/重用拒绝或主动社交绑定。本机现有账户凭据不在测试范围内。

### CLI 真实验收闭环

2026-09-06 用户明确批准以测试邮箱授予隔离 CLI 的 `routes:read` / `runs:start`，验证刷新后撤销，并要求复用现有实现。该次本地 Linux/amd64 流程已完成：

- Chrome 复用邮箱会话批准真实 `nt login` 设备码，原生页面显示设备已连接，CLI 返回 `Account login saved`；独立的后续 `nt routes` 进程通过实际 Refresh Token 刷新读取到该邮箱的路由。
- 通过原有控制台创建专用路由 `identity-acceptance`（`rte_lzvckh6za7ecn5r3h5tnholqby`），原版 `nt start` 成功启动 `run_dribqcsgskbzbkchs4ars2axki`。请求 `127.0.0.1:18080` 并携带该路由 Host 后返回 `200` 及本地后端标记；控制台显示在线及原生 UTC 字节样本。
- CLI 正常中断后返回 `Tunnel stopped` 并以 `0` 结束；同一入口探测变为 `404` 且不再到达后端，回收后无活动运行。永久验收路由保留供用户查看，未删除用户账号或业务数据。
- [真实认证验收测试](../internal/cliauth/local_acceptance_linux_test.go)复用 `NewFileStore`、现有 `cliauth.Client`、`runclient.Client`、已有测试内存 Store 和真实 `/nt` 子进程，不另写 OAuth 交换/撤销协议。默认跳过；只有显式 opt-in、固定回环入口、UID 65532、私密 tmpfs 和正确 issuer/client/resource 绑定才运行。
- 最终在新的 Device Flow 授权后重新运行完整测试，六项均通过：真实刷新与路由读取；磁盘 RT 等于当前缓存新 RT 且不同于刷新前 RT；真实 `nt logout` 成功且凭据文件消失；保留在测试进程内存中的最新 RT 再刷新得到 `invalid_grant`；此前未过期 AT 仍可读取路由；新 CLI 进程明确要求重新登录。没有把轮换失效的旧 RT 冒充退出撤销证据。
- `auth/deploy/local.tunnel.compose.yaml` 的显式 `cli` profile 已固化正确夹具：`/nt`、`/acceptance.test` 和公有 CA 独立只读挂载，凭据只在 4 MiB、`0700`、`noexec` 的 `/cli-account` tmpfs；`TMPDIR` 指向该私密目录供临时公有 CA 使用。单个容器内顺序运行，不能并行消费同一登录。
- 早期夹具曾错误地把 16 MiB 验收二进制复制进 4 MiB 凭据 tmpfs，触发 ENOSPC；另一次只读根目录缺少可写 TMPDIR，Runner 拒绝启动并清理预约。这两次失败不计作成功验收。已移除不完整文件、清理该登录并停止旧容器；修正挂载后重新授权及验证。两个早期临时 CLI 容器已删除，正式 Compose CLI 服务已停止；凭据删除已单独确认，未影响现有 Windows 登录、Google/邮箱 Web Session 或共享测试容器。
- 发现并修复回收后的状态文案误报：无 `current_run` 不代表“从未运行”，现有 12 语言的同一翻译键改为“无活动运行”等对应表达；没有新增历史接口、数据库字段或第二套状态实现。前端已重建、同步嵌入产物并重建本地 Tunnel 镜像。
- 最终验证：真实隔离 PostgreSQL/Redis 下 Go 全套测试、`go vet ./...`、`tunneld`/`nt` 构建通过；Linux live 验收测试的六项断言通过，默认无 opt-in 的网络隔离容器运行明确跳过。auth 全套 221 项：218 通过、3 项 Windows 文件符号链接测试跳过；Web 源测试 29 通过、1 项 POSIX 测试跳过，构建测试 22 通过，嵌入一致性 1 通过。独立审查确认最新 RT 断言、既有客户端复用和 CLI Compose 隔离约束；浏览器已确认更新后的状态文案和未受 CLI 退出影响的邮箱 Web Session。

本次不覆盖 Access Token 实际到期、Windows/ARM64 实机 Device Flow、长连接停止、公开安装器或生产部署。`auth/README.md` 的“Real CLI acceptance”是复现入口，后续每次重新授权后才能再次执行会消费登录的撤销测试。

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
