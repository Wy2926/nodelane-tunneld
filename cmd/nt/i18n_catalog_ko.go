package main

var catalogKO = map[messageID]string{
	msgChooseMode:           "모드 선택",
	msgAnonymousMode:        "익명 터널",
	msgAccountMode:          "계정 로그인",
	msgLoggedIn:             "로그인 저장됨",
	msgLoggedOut:            "로컬 로그인 삭제됨",
	msgLoginURL:             "로그인 URL",
	msgLoginCode:            "기기 코드",
	msgNoRoutes:             "경로 없음",
	msgRoutesLabel:          "경로",
	msgRouteIDLabel:         "경로 ID",
	msgRunIDLabel:           "실행 ID",
	msgStateLabel:           "상태",
	msgRouteSelectionFailed: "일치하는 활성 경로를 하나로 확인할 수 없습니다.",
	msgLoginRequired:        "먼저 nt login으로 로그인하세요.",
	msgOperationFailed:      "작업 실패: %s",
	msgRetryAfter:           "%d초 후 다시 시도하거나 기존 실행을 중지하세요.",
	msgRunStopping:          "터널 중지 중",
	msgRunStopped:           "터널 중지됨",
	msgRunReconnecting:      "다시 연결 중",
	msgStatusOK:             "성공", msgStatusWarning: "경고", msgStatusError: "오류",
	msgDirectCommandHint:       "다음부터는 nt 명령 하나로 바로 사용할 수 있습니다.",
	msgUsage:                   "사용법: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "인수 없이 nt를 실행하면 익명 사용 또는 계정 로그인을 선택합니다.",
	msgHelpLanguage:            "--lang 언어로 표시 언어를 선택하고, ‘nt languages’로 목록을 확인하세요.",
	msgHelpLanguageEnvironment: "NT_LANG이 시스템 언어보다 우선합니다. LC_ALL, LC_MESSAGES, LANG도 지원합니다.",
	msgSupportedLanguages:      "지원 언어", msgMissingLanguage: "--lang에는 언어 코드가 필요합니다",
	msgUnsupportedLanguage: "지원하지 않는 언어 %q; 지원 언어: %s",
	msgInvalidPort:         "로컬 포트는 1에서 65535 사이의 숫자여야 합니다", msgInvalidLocalAddress: "로컬 주소는 포트가 없는 호스트 이름 또는 IP 주소여야 합니다", msgInvalidProtocol: "프로토콜은 http, tcp 또는 udp여야 합니다",

	msgNoInteractiveInput: "대화형 입력을 사용할 수 없습니다. 다음과 같이 프로토콜, 로컬 주소, 포트를 직접 지정하세요: nt anonymous http localhost 3000",

	msgChooseProtocol:     "터널 프로토콜을 선택하세요:",
	msgProtocolNavigation: "↑/↓ 선택 · Enter 확인",

	msgLocalAddressPrompt:      "로컬 주소 [localhost]: ",
	msgLocalAddressDefaultHelp: "비워 두면 localhost 사용",
	msgPortPrompt:              "로컬 포트 [1-65535]: ",
	msgPortRangeHelp:           "1–65535 입력",

	msgConnectingEdge: "NodeLane 에지 노드에 연결 중",

	msgTunnelConnected:   "터널 연결됨",
	msgLocalAddressLabel: "로컬 주소", msgPublicAddressLabel: "공개 주소", msgExpiresAtLabel: "만료 시각",
}
