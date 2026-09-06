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
3. **真实身份联调仅部分完成。** 真实 Google 登录、第二应用 SSO、邮箱验证码注册，以及 Linux 容器内 CLI Device Flow → 跨进程刷新 → 路由运行 → 退出撤销已通过。已有邮箱账号的独立再次登录、错误/过期/重用验证码、Google 主动绑定/冲突隔离、Access Token 真实到期拒绝和浏览器真实退出仍待验收；不把 CLI 退出当作浏览器退出。详见下节和 [auth 操作手册](../../auth/README.md)。
4. **真实部署验收未执行。** DNS/TLS、可信代理、内网管理面、frps 重启/UTC 跨日统计、全新机器上的三种公开安装命令及生产停止传播仍须按规范验收。编译通过不等于 ARM64 实机通过。
5. **0.5.0 发布准备。** 2026-09-06 用户已授权提交并推送当前 Tunnel Git 改动、发布镜像；控制面镜像及 `client-version.txt` 均采用 `0.5.0`，内置新客户端不覆盖旧 `0.4.4` 字节。发布结果须按实际 Git 与 Registry 回读记录；本次不授权生产部署、密钥轮换、提供方修改或真实数据库清理，也不额外触发 `nt-v*` GitHub 客户端 Release。

不要将剩余工作概括成“仅差上线配置”，也不要因全部现有测试通过而宣称全部需求闭环。

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
