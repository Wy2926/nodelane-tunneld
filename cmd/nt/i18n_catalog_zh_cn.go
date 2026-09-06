package main

var catalogZhCN = map[messageID]string{
	msgChooseMode:           "选择模式",
	msgAnonymousMode:        "匿名隧道",
	msgAccountMode:          "登录账号",
	msgLoggedIn:             "已保存账号登录",
	msgLoggedOut:            "已清除本地账号登录",
	msgLoginURL:             "登录网址",
	msgLoginCode:            "设备代码",
	msgNoRoutes:             "暂无路由",
	msgRoutesLabel:          "路由",
	msgRouteIDLabel:         "路由 ID",
	msgRunIDLabel:           "运行 ID",
	msgStateLabel:           "状态",
	msgRouteSelectionFailed: "没有找到唯一匹配的活动路由。",
	msgLoginRequired:        "请先运行 nt login 登录。",
	msgOperationFailed:      "操作失败：%s",
	msgRetryAfter:           "请在 %d 秒后重试，或先停止已有运行。",
	msgRunStopping:          "正在停止隧道",
	msgRunStopped:           "隧道已停止",
	msgRunReconnecting:      "正在重新连接",
	msgStatusOK:             "成功", msgStatusWarning: "警告", msgStatusError: "错误",
	msgDirectCommandHint:       "下次直接运行 nt 即可，一行命令就能使用。",
	msgUsage:                   "用法: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "不带参数运行时，nt 提供匿名使用或登录账号选择。",
	msgHelpLanguage:            "使用 --lang 语言 设置显示语言；运行“nt languages”查看语言列表。",
	msgHelpLanguageEnvironment: "NT_LANG 优先于系统语言；同时支持 LC_ALL、LC_MESSAGES 和 LANG。",
	msgSupportedLanguages:      "支持的语言", msgMissingLanguage: "--lang 后需要提供语言代码",
	msgUnsupportedLanguage: "不支持语言 %q；支持的语言：%s",
	msgInvalidPort:         "本地端口必须是 1 到 65535 之间的数字", msgInvalidLocalAddress: "本地地址必须是不带端口的主机名或 IP 地址", msgInvalidProtocol: "协议必须是 http、tcp 或 udp",

	msgNoInteractiveInput: "当前环境无法交互输入，请直接传入协议、本地地址和端口，例如：nt anonymous http localhost 3000",

	msgChooseProtocol:     "请选择隧道协议：",
	msgProtocolNavigation: "↑/↓ 选择 · Enter 确认",

	msgLocalAddressPrompt:      "本地地址 [localhost]: ",
	msgLocalAddressDefaultHelp: "留空使用 localhost",
	msgPortPrompt:              "本地端口 [1-65535]: ",
	msgPortRangeHelp:           "请输入 1–65535",

	msgConnectingEdge: "正在连接 NodeLane 边缘节点",

	msgTunnelConnected:   "隧道连接成功",
	msgLocalAddressLabel: "本地地址", msgPublicAddressLabel: "公网地址", msgExpiresAtLabel: "到期时间",
}
