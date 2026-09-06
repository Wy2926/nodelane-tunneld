package main

var catalogJA = map[messageID]string{
	msgChooseMode:           "モードを選択",
	msgAnonymousMode:        "匿名トンネル",
	msgAccountMode:          "アカウントにログイン",
	msgLoggedIn:             "ログインを保存しました",
	msgLoggedOut:            "ローカルのログインを削除しました",
	msgLoginURL:             "ログイン URL",
	msgLoginCode:            "デバイスコード",
	msgNoRoutes:             "ルートがありません",
	msgRoutesLabel:          "ルート",
	msgRouteIDLabel:         "ルート ID",
	msgRunIDLabel:           "実行 ID",
	msgStateLabel:           "状態",
	msgRouteSelectionFailed: "一致する有効なルートを一意に特定できません。",
	msgLoginRequired:        "先に nt login でログインしてください。",
	msgOperationFailed:      "操作に失敗しました: %s",
	msgRetryAfter:           "%d 秒後に再試行するか、既存の実行を停止してください。",
	msgRunStopping:          "トンネルを停止中",
	msgRunStopped:           "トンネルを停止しました",
	msgRunReconnecting:      "再接続中",
	msgStatusOK:             "成功", msgStatusWarning: "警告", msgStatusError: "エラー",
	msgDirectCommandHint:       "次回からは nt を実行するだけで使えます。",
	msgUsage:                   "使用方法: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "引数なしの nt は匿名利用またはアカウントログインを選択します。",
	msgHelpLanguage:            "--lang 言語 で表示言語を選択できます。「nt languages」で一覧を表示します。",
	msgHelpLanguageEnvironment: "NT_LANG はシステム言語より優先されます。LC_ALL、LC_MESSAGES、LANG にも対応しています。",
	msgSupportedLanguages:      "対応言語", msgMissingLanguage: "--lang には言語コードが必要です",
	msgUnsupportedLanguage: "言語 %q は対応していません。対応言語: %s",
	msgInvalidPort:         "ローカルポートは 1～65535 の数値で指定してください", msgInvalidLocalAddress: "ローカルアドレスはポートを含まないホスト名または IP アドレスで指定してください", msgInvalidProtocol: "プロトコルは http、tcp、udp のいずれかです",

	msgNoInteractiveInput: "対話入力を利用できません。例のようにプロトコル、ローカルアドレス、ポートを直接指定してください: nt anonymous http localhost 3000",

	msgChooseProtocol:     "トンネルプロトコルを選択してください:",
	msgProtocolNavigation: "↑/↓ で選択 · Enter で確定",

	msgLocalAddressPrompt:      "ローカルアドレス [localhost]: ",
	msgLocalAddressDefaultHelp: "空欄の場合は localhost",
	msgPortPrompt:              "ローカルポート [1-65535]: ",
	msgPortRangeHelp:           "1～65535 を入力",

	msgConnectingEdge: "NodeLane エッジノードに接続中",

	msgTunnelConnected:   "トンネルに接続しました",
	msgLocalAddressLabel: "ローカルアドレス", msgPublicAddressLabel: "公開アドレス", msgExpiresAtLabel: "有効期限",
}
