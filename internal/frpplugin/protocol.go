// Package frpplugin contains the wire contract used by the official frp
// server HTTP plugin API. Keeping this contract outside the HTTP handlers
// makes protocol upgrades explicit and testable.
package frpplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const APIVersion = "0.1.0"

type Operation string

const (
	OpLogin       Operation = "Login"
	OpNewProxy    Operation = "NewProxy"
	OpCloseProxy  Operation = "CloseProxy"
	OpPing        Operation = "Ping"
	OpNewWorkConn Operation = "NewWorkConn"
	OpNewUserConn Operation = "NewUserConn"
)

func (op Operation) Supported() bool {
	switch op {
	case OpLogin, OpNewProxy, OpCloseProxy, OpPing, OpNewWorkConn, OpNewUserConn:
		return true
	default:
		return false
	}
}

// Request mirrors pkg/plugin/server.Request in frp v0.70.0.
type Request struct {
	Version string          `json:"version"`
	Op      Operation       `json:"op"`
	Content json.RawMessage `json:"content"`
}

// DecodeRequest validates both copies of the operation and API version. frps
// puts them in the query string and in the JSON envelope.
func DecodeRequest(reader io.Reader, queryVersion, queryOp string) (Request, error) {
	var request Request
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("decode callback envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return request, errors.New("callback envelope contains multiple JSON values")
		}
		return request, fmt.Errorf("decode callback envelope trailer: %w", err)
	}
	if request.Version != APIVersion || queryVersion != APIVersion {
		return request, fmt.Errorf("unsupported callback API version body=%q query=%q", request.Version, queryVersion)
	}
	if !request.Op.Supported() {
		return request, fmt.Errorf("unsupported callback operation %q", request.Op)
	}
	if queryOp != string(request.Op) {
		return request, fmt.Errorf("callback operation mismatch body=%q query=%q", request.Op, queryOp)
	}
	if len(request.Content) == 0 || string(request.Content) == "null" {
		return request, errors.New("callback content is required")
	}
	return request, nil
}

func (r Request) DecodeContent(target any) error {
	if err := json.Unmarshal(r.Content, target); err != nil {
		return fmt.Errorf("decode %s callback content: %w", r.Op, err)
	}
	return nil
}

// Response mirrors pkg/plugin/server.Response in frp v0.70.0.
type Response struct {
	Reject       bool   `json:"reject"`
	RejectReason string `json:"reject_reason,omitempty"`
	Unchange     bool   `json:"unchange"`
	Content      any    `json:"content,omitempty"`
}

type ClientSpec struct {
	Type           string `json:"type,omitempty"`
	AlwaysAuthPass bool   `json:"always_auth_pass,omitempty"`
}

// LoginContent mirrors the official Login message plus client_address added
// by the server plugin layer. NodeLane requires ClientID and leaves User empty
// so proxy names are not rewritten.
type LoginContent struct {
	Version       string            `json:"version,omitempty"`
	Hostname      string            `json:"hostname,omitempty"`
	OS            string            `json:"os,omitempty"`
	Arch          string            `json:"arch,omitempty"`
	User          string            `json:"user,omitempty"`
	PrivilegeKey  string            `json:"privilege_key,omitempty"`
	Timestamp     int64             `json:"timestamp,omitempty"`
	RunID         string            `json:"run_id,omitempty"`
	ClientID      string            `json:"client_id,omitempty"`
	Metas         map[string]string `json:"metas,omitempty"`
	ClientSpec    ClientSpec        `json:"client_spec,omitempty"`
	PoolCount     int               `json:"pool_count,omitempty"`
	ClientAddress string            `json:"client_address,omitempty"`
}

type UserInfo struct {
	User  string            `json:"user"`
	Metas map[string]string `json:"metas"`
	RunID string            `json:"run_id"`
}

// NewProxyContent includes every NewProxy field in frp v0.70.0 so a modified
// response can be returned without silently dropping official fields.
type NewProxyContent struct {
	User UserInfo `json:"user"`

	ProxyName          string            `json:"proxy_name,omitempty"`
	ProxyType          string            `json:"proxy_type,omitempty"`
	UseEncryption      bool              `json:"use_encryption,omitempty"`
	UseCompression     bool              `json:"use_compression,omitempty"`
	BandwidthLimit     string            `json:"bandwidth_limit,omitempty"`
	BandwidthLimitMode string            `json:"bandwidth_limit_mode,omitempty"`
	Group              string            `json:"group,omitempty"`
	GroupKey           string            `json:"group_key,omitempty"`
	Metas              map[string]string `json:"metas,omitempty"`
	Annotations        map[string]string `json:"annotations,omitempty"`

	RemotePort int `json:"remote_port,omitempty"`

	CustomDomains     []string          `json:"custom_domains,omitempty"`
	Subdomain         string            `json:"subdomain,omitempty"`
	Locations         []string          `json:"locations,omitempty"`
	HTTPUser          string            `json:"http_user,omitempty"`
	HTTPPwd           string            `json:"http_pwd,omitempty"`
	HostHeaderRewrite string            `json:"host_header_rewrite,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	ResponseHeaders   map[string]string `json:"response_headers,omitempty"`
	RouteByHTTPUser   string            `json:"route_by_http_user,omitempty"`

	SecretKey  string   `json:"sk,omitempty"`
	AllowUsers []string `json:"allow_users,omitempty"`

	Multiplexer string `json:"multiplexer,omitempty"`
}

type CloseProxyContent struct {
	User      UserInfo `json:"user"`
	ProxyName string   `json:"proxy_name,omitempty"`
}

type PingContent struct {
	User         UserInfo `json:"user"`
	PrivilegeKey string   `json:"privilege_key,omitempty"`
	Timestamp    int64    `json:"timestamp,omitempty"`
}

type NewWorkConnContent struct {
	User         UserInfo `json:"user"`
	RunID        string   `json:"run_id,omitempty"`
	PrivilegeKey string   `json:"privilege_key,omitempty"`
	Timestamp    int64    `json:"timestamp,omitempty"`
}

type NewUserConnContent struct {
	User       UserInfo `json:"user"`
	ProxyName  string   `json:"proxy_name"`
	ProxyType  string   `json:"proxy_type"`
	RemoteAddr string   `json:"remote_addr"`
}
