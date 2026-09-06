package main

var catalogZhTW = map[messageID]string{
	msgChooseMode:           "選擇模式",
	msgAnonymousMode:        "匿名隧道",
	msgAccountMode:          "登入帳號",
	msgLoggedIn:             "已儲存帳號登入",
	msgLoggedOut:            "已清除本機帳號登入",
	msgLoginURL:             "登入網址",
	msgLoginCode:            "裝置代碼",
	msgNoRoutes:             "尚無路由",
	msgRoutesLabel:          "路由",
	msgRouteIDLabel:         "路由 ID",
	msgRunIDLabel:           "執行 ID",
	msgStateLabel:           "狀態",
	msgRouteSelectionFailed: "找不到唯一符合的使用中路由。",
	msgLoginRequired:        "請先執行 nt login 登入。",
	msgOperationFailed:      "操作失敗：%s",
	msgRetryAfter:           "請在 %d 秒後重試，或先停止既有執行。",
	msgRunStopping:          "正在停止隧道",
	msgRunStopped:           "隧道已停止",
	msgRunReconnecting:      "正在重新連線",
	msgStatusOK:             "成功", msgStatusWarning: "警告", msgStatusError: "錯誤",
	msgDirectCommandHint:       "下次直接執行 nt 即可，一行指令就能使用。",
	msgUsage:                   "用法: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "不帶參數執行時，nt 提供匿名使用或登入帳號選擇。",
	msgHelpLanguage:            "使用 --lang 語言 設定顯示語言；執行「nt languages」查看語言清單。",
	msgHelpLanguageEnvironment: "NT_LANG 優先於系統語言；同時支援 LC_ALL、LC_MESSAGES 和 LANG。",
	msgSupportedLanguages:      "支援的語言", msgMissingLanguage: "--lang 後需要提供語言代碼",
	msgUnsupportedLanguage: "不支援語言 %q；支援的語言：%s",
	msgInvalidPort:         "本機連接埠必須是 1 到 65535 之間的數字", msgInvalidLocalAddress: "本機位址必須是不含連接埠的主機名稱或 IP 位址", msgInvalidProtocol: "協定必須是 http、tcp 或 udp",

	msgNoInteractiveInput: "目前環境無法互動輸入，請直接傳入協定、本機位址和連接埠，例如：nt anonymous http localhost 3000",

	msgChooseProtocol:     "請選擇隧道協定：",
	msgProtocolNavigation: "↑/↓ 選擇 · Enter 確認",

	msgLocalAddressPrompt:      "本機位址 [localhost]: ",
	msgLocalAddressDefaultHelp: "留空使用 localhost",
	msgPortPrompt:              "本機連接埠 [1-65535]: ",
	msgPortRangeHelp:           "請輸入 1–65535",

	msgConnectingEdge: "正在連線 NodeLane 邊緣節點",

	msgTunnelConnected:   "隧道連線成功",
	msgLocalAddressLabel: "本機位址", msgPublicAddressLabel: "公網位址", msgExpiresAtLabel: "到期時間",
}
