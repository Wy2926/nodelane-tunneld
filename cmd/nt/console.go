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
)

const brandArt = `
 _   _  ___  ____  _____ _        _    _   _ _____
| \ | |/ _ \|  _ \| ____| |      / \  | \ | | ____|
|  \| | | | | | | |  _| | |     / _ \ |  \| |  _|
| |\  | |_| | |_| | |___| |___ / ___ \| |\  | |___
|_| \_|\___/|____/|_____|_____/_/   \_\_| \_|_____|
                       T U N N E L`

type consoleUI struct {
	out       io.Writer
	err       io.Writer
	color     bool
	localizer localizer
	mu        sync.Mutex
	statusSet bool
}

func newConsoleUI(out, errOut io.Writer) *consoleUI {
	return &consoleUI{out: out, err: errOut, color: supportsColor(out), localizer: newLocalizer("en")}
}

func (ui *consoleUI) setLocalizer(value localizer) {
	ui.localizer = value
}

func (ui *consoleUI) text(id messageID, values ...any) string {
	return ui.localizer.text(id, values...)
}

func supportsColor(writer io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (ui *consoleUI) paint(code, value string) string {
	if !ui.color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func (ui *consoleUI) clearStatusLocked() {
	if !ui.statusSet {
		return
	}
	if ui.color {
		fmt.Fprint(ui.out, "\r\x1b[2K")
	} else {
		fmt.Fprint(ui.out, "\r")
	}
	ui.statusSet = false
}

func (ui *consoleUI) step(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	fmt.Fprintf(ui.out, "%s %s\n", ui.paint("1;36", "==>"), message)
}

func (ui *consoleUI) success(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	fmt.Fprintf(ui.out, "%s %s\n", ui.paint("1;32", ui.text(msgStatusOK)), ui.paint("1", message))
}

func (ui *consoleUI) warning(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	fmt.Fprintf(ui.err, "%s %s\n", ui.paint("1;33", ui.text(msgStatusWarning)), message)
}

func (ui *consoleUI) failure(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	fmt.Fprintf(ui.err, "%s %s\n", ui.paint("1;31", ui.text(msgStatusError)), message)
}

func (ui *consoleUI) banner() {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	fmt.Fprintln(ui.out, ui.paint("1;36", brandArt))
}

func (ui *consoleUI) detail(label, value string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	fmt.Fprintf(ui.out, "  %s %s\n", ui.paint("2", fmt.Sprintf("%-10s", label)), ui.paint("1;37", value))
}

func (ui *consoleUI) highlightedDetail(label, value string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	fmt.Fprintf(ui.out, "  %s %s\n", ui.paint("1;32", fmt.Sprintf("%-10s", label)), ui.paint("1;32", value))
}

func (ui *consoleUI) instruction(message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	fmt.Fprintf(ui.out, "\n%s\n", ui.paint("1;33", message))
}

func (ui *consoleUI) request(at time.Time, ip, method, address string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.clearStatusLocked()
	ip = safeConsoleField(ip, 64)
	method = safeConsoleField(method, 16)
	address = safeConsoleField(address, 2048)
	methodColor := "1;34"
	switch method {
	case "POST", "PUT", "PATCH":
		methodColor = "1;33"
	case "DELETE":
		methodColor = "1;31"
	}
	fmt.Fprintf(ui.out, "%s  %s  %s  %s\n",
		ui.paint("2", at.Local().Format("2006-01-02 15:04:05")),
		ui.paint("36", fmt.Sprintf("%-39s", ip)),
		ui.paint(methodColor, fmt.Sprintf("%-7s", method)),
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
		ui.paint("1;35", strings.ToUpper(protocol)),
		ui.text(msgTrafficStats,
			ui.paint("1;36", strconv.FormatInt(snapshot.ActiveConnections, 10)),
			ui.paint("36", strconv.FormatInt(snapshot.TotalConnections, 10)),
			ui.paint("1;33", formatBytes(snapshot.ReceivedBytes)),
			ui.paint("1;32", formatBytes(snapshot.SentBytes)),
		),
	)
	if ui.color {
		fmt.Fprintf(ui.out, "\r\x1b[2K%s", line)
	} else {
		fmt.Fprintf(ui.out, "\r%s", line)
	}
	ui.statusSet = true
}

func (ui *consoleUI) endStats() {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if ui.statusSet {
		fmt.Fprintln(ui.out)
		ui.statusSet = false
	}
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
	device := "/dev/tty"
	if runtime.GOOS == "windows" {
		device = "CONIN$"
	}
	if file, err := os.Open(device); err == nil {
		return file, func() { _ = file.Close() }, nil
	}
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return os.Stdin, func() {}, nil
	}
	return nil, func() {}, errors.New(ui.text(msgNoInteractiveInput))
}
