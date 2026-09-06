package main

var catalogAR = map[messageID]string{
	msgChooseMode:           "اختيار الوضع",
	msgAnonymousMode:        "نفق مجهول",
	msgAccountMode:          "تسجيل الدخول",
	msgLoggedIn:             "تم حفظ تسجيل الدخول",
	msgLoggedOut:            "تم مسح تسجيل الدخول المحلي",
	msgLoginURL:             "رابط تسجيل الدخول",
	msgLoginCode:            "رمز الجهاز",
	msgNoRoutes:             "لا توجد مسارات",
	msgRoutesLabel:          "المسارات",
	msgRouteIDLabel:         "معرف المسار",
	msgRunIDLabel:           "معرف التشغيل",
	msgStateLabel:           "الحالة",
	msgRouteSelectionFailed: "لم يتم العثور على مسار نشط مطابق وفريد.",
	msgLoginRequired:        "سجل الدخول أولاً باستخدام nt login.",
	msgOperationFailed:      "فشلت العملية: %s",
	msgRetryAfter:           "أعد المحاولة بعد %d ثانية أو أوقف تشغيلاً حالياً.",
	msgRunStopping:          "جار إيقاف النفق",
	msgRunStopped:           "تم إيقاف النفق",
	msgRunReconnecting:      "جار إعادة الاتصال",
	msgStatusOK:             "تم", msgStatusWarning: "تحذير", msgStatusError: "خطأ",
	msgDirectCommandHint:       "في المرة القادمة، شغّل nt فقط؛ أمر واحد يكفي.",
	msgUsage:                   "الاستخدام: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "دون معاملات، يتيح nt اختيار نفق مجهول أو تسجيل الدخول إلى الحساب.",
	msgHelpLanguage:            "استخدم --lang اللغة لاختيار لغة العرض؛ نفّذ «nt languages» لعرض القائمة.",
	msgHelpLanguageEnvironment: "يتجاوز NT_LANG لغة النظام. كما تُدعم LC_ALL وLC_MESSAGES وLANG.",
	msgSupportedLanguages:      "اللغات المدعومة", msgMissingLanguage: "يتطلب --lang رمز لغة",
	msgUnsupportedLanguage: "اللغة %q غير مدعومة؛ اللغات المدعومة: %s",
	msgInvalidPort:         "يجب أن يكون المنفذ المحلي رقماً من 1 إلى 65535", msgInvalidLocalAddress: "يجب أن يكون العنوان المحلي اسم مضيف أو عنوان IP من دون منفذ", msgInvalidProtocol: "يجب أن يكون البروتوكول http أو tcp أو udp",

	msgNoInteractiveInput: "الإدخال التفاعلي غير متاح؛ مرّر البروتوكول والعنوان المحلي والمنفذ مباشرة، مثلاً: nt anonymous http localhost 3000",

	msgChooseProtocol:     "اختر بروتوكول النفق:",
	msgProtocolNavigation: "↑/↓ للاختيار · Enter للتأكيد",

	msgLocalAddressPrompt:      "العنوان المحلي [localhost]: ",
	msgLocalAddressDefaultHelp: "اتركه فارغًا لاستخدام localhost",
	msgPortPrompt:              "المنفذ المحلي [1-65535]: ",
	msgPortRangeHelp:           "أدخل قيمة من 1 إلى 65535",

	msgConnectingEdge: "جارٍ الاتصال بعقدة NodeLane طرفية",

	msgTunnelConnected:   "تم اتصال النفق",
	msgLocalAddressLabel: "العنوان المحلي", msgPublicAddressLabel: "العنوان العام", msgExpiresAtLabel: "ينتهي في",
}
