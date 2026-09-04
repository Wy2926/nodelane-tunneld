# frp 连接与回调排障

## 先区分两个阶段

`nt` 内嵌的 frp client service 到隧道上线依次经过：

1. TCP 连接到 frps `bindPort`（默认 7000）；
2. TLS 握手；
3. yamux 建立多路复用会话；
4. 内嵌 frp client 发送 Login，frps 才调用 tunneld 的 HTTP 插件；
5. 内嵌 frp client 创建代理，frps 调用 NewProxy；
6. tunneld 把隧道状态改为 online。

因此，frps 的 `yamux: Invalid protocol version: 71` 发生在第 3 步，尚未
进入回调。十进制 `71` 是 ASCII 字符 `G`，通常是 TLS 内出现了
`GET ...`。常见来源是：

- 把 WSS 直接连到 frps 的 TLS bindPort，而不是先由反向代理终止 WSS；
- 1Panel、负载均衡或监控系统对 7000 做 HTTPS GET 健康检查；
- DNS/端口转发把 7000 指向了 HTTPS 站点而不是 frps；
- 客户端与 frps 版本不配套或额外手写了配置。

项目将官方 frp 0.70.0 client service 编译进 `nt`，并显式设置
`transport.protocol = "tcp"`、TLS、yamux 和 wire v1。不要在
1Panel 中给 7000 配置 HTTP/HTTPS 反向代理；它应是四层 TCP 端口。

后续出现的 `non-TLS connection received on a TlsOnly server` 代表另一个
连接以明文访问了 7000，通常也是端口扫描或错误的健康检查。它并不
表示刚才已经完成 TLS 的连接降级成了明文。

## 回调检查

官方 frps 0.70.0 的回调请求同时在 query 和 JSON body 中携带
`version=0.1.0` 与 `op`。tunneld 会核对两份值，并按官方类型解码
Login、NewProxy、CloseProxy、Ping、NewWorkConn 和 NewUserConn。

客户端使用 `clientID`，不会修改代理名；服务端要求 `user` 为空且代理名
必须精确匹配租约。首次 Login 的 `run_id` 可能为空，因为 frps 在插件放行后才分配
它；服务端会先按 tunnel id 关联来源 IP，再在 NewProxy 时关联 run id。

frps 和内嵌 frp client 两端必须同时声明：

```toml
auth.additionalScopes = ["HeartBeats", "NewWorkConns"]
```

只在 frps 端启用会导致 Login/NewProxy 看似成功，但实际工作连接报
`token in NewWorkConn doesn't match token from configuration`。项目生成的
客户端配置已经显式写入这两个 scope。

临时打开详细日志：

```dotenv
LOG_LEVEL=debug
```

```sh
docker compose --env-file .env -f deploy/compose.registry.yaml up -d
docker compose --env-file .env -f deploy/compose.registry.yaml logs -f tunneld
```

关注以下事件：

- `frp callback received`：tunneld 已收到请求；
- `frp callback allowed`：协议、凭据、租约和网络策略均通过；
- `frp callback rejected`：查看 `stage` 和 `error`；
- `internal endpoint access rejected`：frps 没有从本机回环访问，或请求
  带了转发头；
- `tunnel reserved` 后没有任何 callback：问题仍在 TCP/TLS/yamux 阶段；
- Login allowed 但 NewProxy rejected：查看 `proxy_name`、代理类型、
  session metadata、子域名或远端端口。

`X-Frp-Reqid` 被记录为 `frp_request_id`，可以把 frps 的一次插件报错和
tunneld 的拒绝日志关联起来。日志不会记录 FRP token、客户端 token 或
隧道 JWT。

## 网络边界

当前生产配置要求 frps 与 tunneld 都使用 host network，frps 回调地址
为 `127.0.0.1:9000`。tunneld 会拒绝非回环来源和带
`X-Forwarded-For`/`X-Real-IP` 的 `/internal/*` 请求。若 frps 仍在 bridge
network，`127.0.0.1` 指向 frps 容器自身，回调不会到达 tunneld；先修正
frps 网络模式，不要把内部回调暴露到公网来绕过这个边界。
