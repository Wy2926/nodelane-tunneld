package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type trafficSnapshot struct {
	ActiveConnections int64
	TotalConnections  int64
	ReceivedBytes     uint64
	SentBytes         uint64
}

func expectedForwardingError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed)
}

const httpUpstreamWarning = "http-upstream"

type forwardingRequestState struct {
	failed bool
}

type forwardingRequestStateKey struct{}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
	onStatus   func(int)
}

func (writer *statusResponseWriter) WriteHeader(statusCode int) {
	// Informational responses may be followed by the final response status.
	if statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols {
		writer.ResponseWriter.WriteHeader(statusCode)
		return
	}
	if writer.statusCode != 0 {
		return
	}
	writer.statusCode = statusCode
	writer.ResponseWriter.WriteHeader(statusCode)
	if writer.onStatus != nil {
		writer.onStatus(statusCode)
	}
}

func (writer *statusResponseWriter) Write(data []byte) (int, error) {
	if writer.statusCode == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *statusResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *statusResponseWriter) reportDefaultStatus() {
	if writer.statusCode != 0 {
		return
	}
	writer.statusCode = http.StatusOK
	if writer.onStatus != nil {
		writer.onStatus(writer.statusCode)
	}
}

type trafficCounters struct {
	active   atomic.Int64
	total    atomic.Int64
	received atomic.Uint64
	sent     atomic.Uint64
}

func (c *trafficCounters) snapshot() trafficSnapshot {
	return trafficSnapshot{
		ActiveConnections: c.active.Load(),
		TotalConnections:  c.total.Load(),
		ReceivedBytes:     c.received.Load(),
		SentBytes:         c.sent.Load(),
	}
}

type trafficMonitor struct {
	port      int
	counters  *trafficCounters
	closeOnce sync.Once
	closeFunc func() error
	closeErr  error
}

func (m *trafficMonitor) Close() error {
	m.closeOnce.Do(func() { m.closeErr = m.closeFunc() })
	return m.closeErr
}

func (m *trafficMonitor) Snapshot() trafficSnapshot {
	return m.counters.snapshot()
}

func startTrafficMonitor(ctx context.Context, protocol, targetHost string, targetPort int, ui *consoleUI) (*trafficMonitor, error) {
	switch protocol {
	case "http":
		return startHTTPMonitor(ctx, targetHost, targetPort, ui)
	case "tcp":
		return startTCPMonitor(ctx, targetHost, targetPort, ui)
	case "udp":
		return startUDPMonitor(ctx, targetHost, targetPort, ui)
	default:
		return nil, fmt.Errorf("%s", ui.text(msgUnsupportedMonitorProtocol, protocol))
	}
}

func startHTTPMonitor(ctx context.Context, targetHost string, targetPort int, ui *consoleUI) (*trafficMonitor, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%s", ui.text(msgStartHTTPMonitorFailed, err))
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(targetHost, strconv.Itoa(targetPort))}
	proxy := &httputil.ReverseProxy{Rewrite: func(proxyRequest *httputil.ProxyRequest) {
		proxyRequest.SetURL(target)
		proxyRequest.Out.Host = proxyRequest.In.Host
		// FRP and the public reverse proxy have already supplied the original
		// forwarding headers. Preserve them instead of appending this local hop.
		for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
			if values, ok := proxyRequest.In.Header[header]; ok {
				proxyRequest.Out.Header[header] = append([]string(nil), values...)
			}
		}
	}}
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
		if state, ok := request.Context().Value(forwardingRequestStateKey{}).(*forwardingRequestState); ok {
			state.failed = true
		}
		if !expectedForwardingError(proxyErr) {
			ui.warningOnce(httpUpstreamWarning, ui.text(msgHTTPServiceUnavailable, proxyErr))
		}
		http.Error(writer, ui.text(msgHTTPServiceUnavailableBody), http.StatusBadGateway)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		address := request.Host + request.URL.RequestURI()
		ip := requestIP(request)
		method := request.Method
		responseWriter := &statusResponseWriter{
			ResponseWriter: writer,
			onStatus: func(statusCode int) {
				ui.request(startedAt, ip, method, statusCode, address)
			},
		}
		state := &forwardingRequestState{}
		request = request.WithContext(context.WithValue(request.Context(), forwardingRequestStateKey{}, state))
		proxy.ServeHTTP(responseWriter, request)
		responseWriter.reportDefaultStatus()
		if !state.failed {
			ui.resetWarning(httpUpstreamWarning)
		}
	})
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveErr := server.Serve(listener)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			ui.warning(ui.text(msgHTTPMonitorStopped, serveErr))
		}
	}()
	counters := &trafficCounters{}
	monitor := &trafficMonitor{port: listener.Addr().(*net.TCPAddr).Port, counters: counters}
	monitor.closeFunc = func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		<-done
		return err
	}
	go func() {
		<-ctx.Done()
		_ = monitor.Close()
	}()
	return monitor, nil
}

func requestIP(request *http.Request) string {
	if value := strings.TrimSpace(request.Header.Get("X-Real-IP")); value != "" {
		return value
	}
	if forwarded := request.Header.Values("X-Forwarded-For"); len(forwarded) > 0 {
		parts := strings.Split(strings.Join(forwarded, ","), ",")
		if value := strings.TrimSpace(parts[len(parts)-1]); value != "" {
			return value
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

type tcpMonitor struct {
	listener net.Listener
	target   string
	counters *trafficCounters
	ui       *consoleUI
	mu       sync.Mutex
	open     map[net.Conn]struct{}
	wg       sync.WaitGroup
	closed   chan struct{}
	once     sync.Once
}

func startTCPMonitor(ctx context.Context, targetHost string, targetPort int, ui *consoleUI) (*trafficMonitor, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%s", ui.text(msgStartTCPMonitorFailed, err))
	}
	forwarder := &tcpMonitor{
		listener: listener,
		target:   net.JoinHostPort(targetHost, strconv.Itoa(targetPort)),
		counters: &trafficCounters{},
		ui:       ui,
		open:     make(map[net.Conn]struct{}),
		closed:   make(chan struct{}),
	}
	forwarder.wg.Add(1)
	go forwarder.acceptLoop()
	monitor := &trafficMonitor{port: listener.Addr().(*net.TCPAddr).Port, counters: forwarder.counters}
	monitor.closeFunc = forwarder.close
	go func() {
		<-ctx.Done()
		_ = monitor.Close()
	}()
	return monitor, nil
}

func (m *tcpMonitor) acceptLoop() {
	defer m.wg.Done()
	for {
		client, err := m.listener.Accept()
		if err != nil {
			select {
			case <-m.closed:
				return
			default:
				m.ui.warning(m.ui.text(msgTCPListenFailed, err))
				continue
			}
		}
		m.wg.Add(1)
		go m.forward(client)
	}
}

func (m *tcpMonitor) forward(client net.Conn) {
	defer m.wg.Done()
	upstream, err := net.DialTimeout("tcp", m.target, 3*time.Second)
	if err != nil {
		_ = client.Close()
		m.ui.warning(m.ui.text(msgTCPServiceUnavailable, err))
		return
	}
	if !m.track(client, upstream) {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	defer func() {
		_ = client.Close()
		_ = upstream.Close()
		m.untrack(client, upstream)
	}()
	m.counters.active.Add(1)
	m.counters.total.Add(1)
	defer m.counters.active.Add(-1)

	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(counterWriter{writer: upstream, counter: &m.counters.received}, client)
		closeWrite(upstream)
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(counterWriter{writer: client, counter: &m.counters.sent}, upstream)
		closeWrite(client)
		finished <- struct{}{}
	}()
	<-finished
	<-finished
}

type counterWriter struct {
	writer  io.Writer
	counter *atomic.Uint64
}

func (writer counterWriter) Write(data []byte) (int, error) {
	n, err := writer.writer.Write(data)
	writer.counter.Add(uint64(n))
	return n, err
}

func closeWrite(connection net.Conn) {
	if tcpConnection, ok := connection.(*net.TCPConn); ok {
		_ = tcpConnection.CloseWrite()
		return
	}
	_ = connection.Close()
}

func (m *tcpMonitor) track(connections ...net.Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.closed:
		return false
	default:
	}
	for _, connection := range connections {
		m.open[connection] = struct{}{}
	}
	return true
}

func (m *tcpMonitor) untrack(connections ...net.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, connection := range connections {
		delete(m.open, connection)
	}
}

func (m *tcpMonitor) close() error {
	m.once.Do(func() {
		close(m.closed)
		_ = m.listener.Close()
		m.mu.Lock()
		for connection := range m.open {
			_ = connection.Close()
		}
		m.mu.Unlock()
	})
	m.wg.Wait()
	return nil
}

type udpSession struct {
	key        string
	clientAddr *net.UDPAddr
	upstream   *net.UDPConn
	lastSeen   atomic.Int64
}

type udpMonitor struct {
	listener *net.UDPConn
	target   *net.UDPAddr
	counters *trafficCounters
	ui       *consoleUI
	mu       sync.Mutex
	sessions map[string]*udpSession
	wg       sync.WaitGroup
	closed   chan struct{}
	once     sync.Once
}

func startUDPMonitor(ctx context.Context, targetHost string, targetPort int, ui *consoleUI) (*trafficMonitor, error) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return nil, fmt.Errorf("%s", ui.text(msgStartUDPMonitorFailed, err))
	}
	target, err := net.ResolveUDPAddr("udp", net.JoinHostPort(targetHost, strconv.Itoa(targetPort)))
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("%s", ui.text(msgUDPServiceUnavailable, err))
	}
	forwarder := &udpMonitor{
		listener: listener,
		target:   target,
		counters: &trafficCounters{},
		ui:       ui,
		sessions: make(map[string]*udpSession),
		closed:   make(chan struct{}),
	}
	forwarder.wg.Add(2)
	go forwarder.receiveLoop()
	go forwarder.expireLoop()
	monitor := &trafficMonitor{port: listener.LocalAddr().(*net.UDPAddr).Port, counters: forwarder.counters}
	monitor.closeFunc = forwarder.close
	go func() {
		<-ctx.Done()
		_ = monitor.Close()
	}()
	return monitor, nil
}

func (m *udpMonitor) receiveLoop() {
	defer m.wg.Done()
	buffer := make([]byte, 64<<10)
	for {
		n, clientAddr, err := m.listener.ReadFromUDP(buffer)
		if err != nil {
			select {
			case <-m.closed:
				return
			default:
				m.ui.warning(m.ui.text(msgUDPListenFailed, err))
				continue
			}
		}
		session, err := m.sessionFor(clientAddr)
		if err != nil {
			m.ui.warning(m.ui.text(msgUDPServiceUnavailable, err))
			continue
		}
		session.lastSeen.Store(time.Now().UnixNano())
		m.counters.received.Add(uint64(n))
		if _, err := session.upstream.Write(buffer[:n]); err != nil {
			m.removeSession(session)
		}
	}
}

func (m *udpMonitor) sessionFor(clientAddr *net.UDPAddr) (*udpSession, error) {
	key := clientAddr.String()
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.closed:
		return nil, net.ErrClosed
	default:
	}
	if session := m.sessions[key]; session != nil {
		return session, nil
	}
	upstream, err := net.DialUDP("udp", nil, m.target)
	if err != nil {
		return nil, err
	}
	clientCopy := *clientAddr
	session := &udpSession{key: key, clientAddr: &clientCopy, upstream: upstream}
	session.lastSeen.Store(time.Now().UnixNano())
	m.sessions[key] = session
	m.counters.active.Add(1)
	m.counters.total.Add(1)
	m.wg.Add(1)
	go m.responseLoop(session)
	return session, nil
}

func (m *udpMonitor) responseLoop(session *udpSession) {
	defer m.wg.Done()
	buffer := make([]byte, 64<<10)
	for {
		n, err := session.upstream.Read(buffer)
		if err != nil {
			m.removeSession(session)
			return
		}
		session.lastSeen.Store(time.Now().UnixNano())
		written, err := m.listener.WriteToUDP(buffer[:n], session.clientAddr)
		m.counters.sent.Add(uint64(written))
		if err != nil {
			m.removeSession(session)
			return
		}
	}
}

func (m *udpMonitor) expireLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.closed:
			return
		case now := <-ticker.C:
			cutoff := now.Add(-60 * time.Second).UnixNano()
			m.mu.Lock()
			var expired []*udpSession
			for _, session := range m.sessions {
				if session.lastSeen.Load() < cutoff {
					delete(m.sessions, session.key)
					m.counters.active.Add(-1)
					expired = append(expired, session)
				}
			}
			m.mu.Unlock()
			for _, session := range expired {
				_ = session.upstream.Close()
			}
		}
	}
}

func (m *udpMonitor) removeSession(session *udpSession) {
	m.mu.Lock()
	if m.sessions[session.key] == session {
		delete(m.sessions, session.key)
		m.counters.active.Add(-1)
	}
	m.mu.Unlock()
	_ = session.upstream.Close()
}

func (m *udpMonitor) close() error {
	m.once.Do(func() {
		close(m.closed)
		_ = m.listener.Close()
		m.mu.Lock()
		var sessions []*udpSession
		for _, session := range m.sessions {
			sessions = append(sessions, session)
		}
		m.sessions = make(map[string]*udpSession)
		m.counters.active.Store(0)
		m.mu.Unlock()
		for _, session := range sessions {
			_ = session.upstream.Close()
		}
	})
	m.wg.Wait()
	return nil
}
