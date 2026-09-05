package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

type consoleStyles struct {
	brand        lipgloss.Style
	brandMiddle  lipgloss.Style
	brandAccent  lipgloss.Style
	step         lipgloss.Style
	success      lipgloss.Style
	warning      lipgloss.Style
	failure      lipgloss.Style
	muted        lipgloss.Style
	value        lipgloss.Style
	public       lipgloss.Style
	protocol     lipgloss.Style
	methodRead   lipgloss.Style
	methodWrite  lipgloss.Style
	methodDelete lipgloss.Style
}

func newConsoleStyles() consoleStyles {
	return consoleStyles{
		brand:        lipgloss.NewStyle().Foreground(lipgloss.BrightCyan).Bold(true),
		brandMiddle:  lipgloss.NewStyle().Foreground(lipgloss.BrightBlue).Bold(true),
		brandAccent:  lipgloss.NewStyle().Foreground(lipgloss.BrightMagenta).Bold(true),
		step:         lipgloss.NewStyle().Foreground(lipgloss.BrightCyan).Bold(true),
		success:      lipgloss.NewStyle().Foreground(lipgloss.BrightGreen).Bold(true),
		warning:      lipgloss.NewStyle().Foreground(lipgloss.BrightYellow).Bold(true),
		failure:      lipgloss.NewStyle().Foreground(lipgloss.BrightRed).Bold(true),
		muted:        lipgloss.NewStyle().Foreground(lipgloss.BrightBlack),
		value:        lipgloss.NewStyle().Foreground(lipgloss.BrightWhite),
		public:       lipgloss.NewStyle().Foreground(lipgloss.BrightGreen).Bold(true),
		protocol:     lipgloss.NewStyle().Foreground(lipgloss.BrightMagenta).Bold(true),
		methodRead:   lipgloss.NewStyle().Foreground(lipgloss.BrightBlue).Bold(true),
		methodWrite:  lipgloss.NewStyle().Foreground(lipgloss.BrightYellow).Bold(true),
		methodDelete: lipgloss.NewStyle().Foreground(lipgloss.BrightRed).Bold(true),
	}
}

type consoleUI struct {
	out         io.Writer
	err         io.Writer
	color       bool
	interactive bool
	styles      consoleStyles
	localizer   localizer
	mu          sync.Mutex
	statusSet   bool
	bannerSet   bool
	warnings    map[string]struct{}
}

type terminalFile interface {
	io.ReadWriteCloser
	Fd() uintptr
}

type terminalColorWriter struct {
	*colorprofile.Writer
	terminal terminalFile
}

func (writer *terminalColorWriter) Read(data []byte) (int, error) {
	return writer.terminal.Read(data)
}

func (writer *terminalColorWriter) Close() error {
	return writer.terminal.Close()
}

func (writer *terminalColorWriter) Fd() uintptr {
	return writer.terminal.Fd()
}

func newConsoleUI(out, errOut io.Writer) *consoleUI {
	enableWindowsANSI(out)
	enableWindowsANSI(errOut)
	return &consoleUI{
		out:         colorWriter(out),
		err:         colorWriter(errOut),
		color:       supportsColor(out),
		interactive: isInteractiveWriter(out),
		styles:      newConsoleStyles(),
		localizer:   newLocalizer("en"),
		warnings:    make(map[string]struct{}),
	}
}

func colorWriter(writer io.Writer) io.Writer {
	profileWriter := colorprofile.NewWriter(writer, os.Environ())
	if supportsColor(writer) && os.Getenv("FORCE_COLOR") != "" && profileWriter.Profile < colorprofile.ANSI {
		profileWriter.Profile = colorprofile.ANSI
	}
	if terminal, ok := writer.(terminalFile); ok {
		return &terminalColorWriter{Writer: profileWriter, terminal: terminal}
	}
	return profileWriter
}

func enableWindowsANSI(writer io.Writer) {
	if file, ok := writer.(*os.File); ok {
		lipgloss.EnableLegacyWindowsANSI(file)
	}
}

func (ui *consoleUI) setLocalizer(value localizer) {
	ui.localizer = value
}

func (ui *consoleUI) text(id messageID, values ...any) string {
	return ui.localizer.text(id, values...)
}

func isInteractiveWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func supportsColor(writer io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return isInteractiveWriter(writer)
}

func (ui *consoleUI) clearStatusLocked() {
	if !ui.statusSet {
		return
	}
	if ui.color {
		_, _ = fmt.Fprintf(ui.out, "\r%s", ansi.EraseEntireLine)
	} else {
		_, _ = fmt.Fprint(ui.out, "\r")
	}
	ui.statusSet = false
}

func (ui *consoleUI) step(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	_, _ = fmt.Fprintf(ui.out, "%s %s\n", ui.styles.step.Render("==>"), message)
}

func (ui *consoleUI) success(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	_, _ = fmt.Fprintf(ui.out, "%s %s\n", ui.styles.success.Render(ui.text(msgStatusOK)), ui.styles.success.Render(message))
}

func (ui *consoleUI) warning(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.warningLocked(message)
}

func (ui *consoleUI) warningLocked(message string) {
	ui.clearStatusLocked()
	_, _ = fmt.Fprintf(ui.out, "%s %s\n", ui.styles.warning.Render(ui.text(msgStatusWarning)), message)
}

func (ui *consoleUI) warningOnce(key, message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if _, exists := ui.warnings[key]; exists {
		return
	}
	ui.warnings[key] = struct{}{}
	ui.warningLocked(message)
}

func (ui *consoleUI) resetWarning(key string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	delete(ui.warnings, key)
}

func (ui *consoleUI) failure(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	_, _ = fmt.Fprintf(ui.err, "%s %s\n", ui.styles.failure.Render(ui.text(msgStatusError)), message)
}

func (ui *consoleUI) banner() {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if ui.bannerSet {
		return
	}
	ui.bannerSet = true
	ui.clearStatusLocked()
	lines := []struct {
		style lipgloss.Style
		text  string
	}{
		{ui.styles.brand, ` _   _ _____`},
		{ui.styles.brand, `| \ | |_   _|`},
		{ui.styles.brandMiddle, `|  \| | | |`},
		{ui.styles.brandMiddle, `| |\  | | |`},
		{ui.styles.brandAccent, `|_| \_| |_|`},
	}
	for _, line := range lines {
		_, _ = fmt.Fprintln(ui.out, line.style.Render(line.text))
	}
	_, _ = fmt.Fprintln(ui.out, ui.styles.brandAccent.Render("NodeLane Tunnel"))
	_, _ = fmt.Fprintln(ui.out, ui.styles.muted.Render(ui.text(msgDirectCommandHint)))
}

func (ui *consoleUI) detail(label, value string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	_, _ = fmt.Fprintf(ui.out, "  %s %s\n", ui.styles.muted.Render(fmt.Sprintf("%-10s", label)), ui.styles.value.Render(value))
}

func (ui *consoleUI) highlightedDetail(label, value string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	_, _ = fmt.Fprintf(ui.out, "  %s %s\n", ui.styles.public.Render(fmt.Sprintf("%-10s", label)), ui.styles.public.Render(value))
}

func (ui *consoleUI) instruction(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	_, _ = fmt.Fprintf(ui.out, "\n%s\n", ui.styles.warning.Render(message))
}

func (ui *consoleUI) request(at time.Time, ip, method string, statusCode int, address string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	ip = safeConsoleField(ip, 64)
	method = safeConsoleField(method, 16)
	address = safeConsoleField(address, 2048)
	methodStyle := ui.styles.methodRead
	switch method {
	case "POST", "PUT", "PATCH":
		methodStyle = ui.styles.methodWrite
	case "DELETE":
		methodStyle = ui.styles.methodDelete
	}
	statusStyle := ui.styles.success
	switch {
	case statusCode >= 500:
		statusStyle = ui.styles.failure
	case statusCode >= 400:
		statusStyle = ui.styles.warning
	case statusCode >= 300:
		statusStyle = ui.styles.step
	}
	_, _ = fmt.Fprintf(ui.out, "%s  %s  %s  %s  %s\n",
		ui.styles.muted.Render(at.Local().Format("2006-01-02 15:04:05")),
		ui.styles.step.Render(fmt.Sprintf("%-39s", ip)),
		methodStyle.Render(fmt.Sprintf("%-7s", method)),
		statusStyle.Render(fmt.Sprintf("%3d", statusCode)),
		address,
	)
}

func safeConsoleField(value string, max int) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	characters := []rune(value)
	if len(characters) > max {
		return string(characters[:max]) + "..."
	}
	return value
}

func (ui *consoleUI) stats(protocol string, snapshot trafficSnapshot) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	line := fmt.Sprintf("%s  %s",
		ui.styles.protocol.Render(strings.ToUpper(protocol)),
		ui.text(msgTrafficStats,
			ui.styles.step.Render(strconv.FormatInt(snapshot.ActiveConnections, 10)),
			ui.styles.step.Render(strconv.FormatInt(snapshot.TotalConnections, 10)),
			ui.styles.warning.Render(formatBytes(snapshot.ReceivedBytes)),
			ui.styles.success.Render(formatBytes(snapshot.SentBytes)),
		),
	)
	if !ui.interactive {
		_, _ = fmt.Fprintln(ui.out, line)
		return
	}
	if ui.color {
		_, _ = fmt.Fprintf(ui.out, "\r%s%s", ansi.EraseEntireLine, line)
	} else {
		_, _ = fmt.Fprintf(ui.out, "\r%s", line)
	}
	ui.statusSet = true
}

func (ui *consoleUI) endStats() {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if ui.statusSet {
		_, _ = fmt.Fprintln(ui.out)
		ui.statusSet = false
	}
}

func nodeLaneFormTheme() huh.Theme {
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		theme := huh.ThemeBase(isDark)
		theme.Focused.Title = theme.Focused.Title.Foreground(lipgloss.BrightCyan).Bold(true)
		theme.Focused.Description = theme.Focused.Description.Foreground(lipgloss.BrightBlack)
		theme.Focused.SelectSelector = theme.Focused.SelectSelector.Foreground(lipgloss.BrightCyan).Bold(true)
		theme.Focused.Option = theme.Focused.Option.Foreground(lipgloss.BrightWhite)
		theme.Focused.TextInput.Cursor = theme.Focused.TextInput.Cursor.Foreground(lipgloss.BrightCyan)
		theme.Focused.TextInput.Prompt = theme.Focused.TextInput.Prompt.Foreground(lipgloss.BrightCyan)
		theme.Focused.TextInput.Placeholder = theme.Focused.TextInput.Placeholder.Foreground(lipgloss.BrightBlack)
		theme.Focused.ErrorIndicator = theme.Focused.ErrorIndicator.Foreground(lipgloss.BrightRed)
		theme.Focused.ErrorMessage = theme.Focused.ErrorMessage.Foreground(lipgloss.BrightRed)
		theme.Blurred = theme.Focused
		theme.Blurred.Base = theme.Blurred.Base.BorderStyle(lipgloss.HiddenBorder())
		theme.Blurred.Title = theme.Blurred.Title.Foreground(lipgloss.BrightBlack).Bold(false)
		theme.Blurred.TextInput.Text = theme.Blurred.TextInput.Text.Foreground(lipgloss.BrightGreen)
		theme.Help.ShortKey = theme.Help.ShortKey.Foreground(lipgloss.BrightBlack)
		theme.Help.ShortDesc = theme.Help.ShortDesc.Foreground(lipgloss.BrightBlack)
		return theme
	})
}

func formatBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := uint64(unit), 0
	for amount := value / unit; amount >= unit && exponent < 4; amount /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

func openInteractiveInput(ui *consoleUI) (io.Reader, func(), error) {
	stdinIsTerminal := false
	if info, err := os.Stdin.Stat(); err == nil {
		stdinIsTerminal = info.Mode()&os.ModeCharDevice != 0
	}
	return selectInteractiveInput(ui, os.Stdin, stdinIsTerminal, openConsoleDevice)
}

func openConsoleDevice() (io.ReadCloser, error) {
	device := "/dev/tty"
	if runtime.GOOS == "windows" {
		device = "CONIN$"
	}
	return os.OpenFile(device, os.O_RDWR, 0)
}

func selectInteractiveInput(ui *consoleUI, stdin io.Reader, stdinIsTerminal bool, openConsole func() (io.ReadCloser, error)) (io.Reader, func(), error) {
	if stdinIsTerminal {
		return stdin, func() {}, nil
	}
	if console, err := openConsole(); err == nil {
		return console, func() { _ = console.Close() }, nil
	}
	return nil, func() {}, errors.New(ui.text(msgNoInteractiveInput))
}
