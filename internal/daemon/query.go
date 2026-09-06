package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/tomasWade/octl/internal/plugins"
)

// queryChannel 是 query 子命令使用的订阅频道名：daemon 对它没有任何推送
// 逻辑（既非 "snapshot" 也非 "view"），连接注册后不会有初始数据推下来，
// 正好用作"纯请求-响应"式连接。
const queryChannel = "query"

// ErrDaemonConnect 表示无法连接 daemon（socket 不存在或连接被拒）。
var ErrDaemonConnect = errors.New("daemon unreachable")

// ErrQueryTimeout 表示等待 daemon 应答超时。
var ErrQueryTimeout = errors.New("daemon response timeout")

// dialSubscribed 建立到 daemon 的连接、设置全流程 deadline 并完成 subscribe
// 握手（订阅无推送的 query 频道并携带协议版本）。QueryOnce 与 ActionOnce
// 共用；socketPath 为空时使用默认路径 ~/.local/share/opencode/octl.sock。
// 调用方负责关闭返回的连接。
func dialSubscribed(socketPath string, timeout time.Duration) (net.Conn, *bufio.Reader, error) {
	sockPath := socketPath
	if sockPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fmt.Errorf("home directory: %w", err)
		}
		sockPath = filepath.Join(home, socketDirPath, socketFileName)
	}

	conn, err := net.DialTimeout("unix", sockPath, timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: dial %s: %v", ErrDaemonConnect, sockPath, err)
	}

	// 一次性交互，单条 deadline 覆盖读写全程即可，无需更细的粒度。
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("set deadline: %w", err)
	}

	sub := SubscribeMsg{
		Type:     "subscribe",
		Channels: []string{queryChannel},
		Version:  plugins.ProtocolMD5(),
	}
	if err := json.NewEncoder(conn).Encode(&sub); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("send subscribe: %w", err)
	}

	return conn, bufio.NewReader(conn), nil
}

// QueryOnce 在一条新连接上完成"订阅握手 → request → 收 response"的一次性
// 交互，供 CLI（octl query）使用，不依赖 SocketClient 的常驻读循环。
//
// socketPath 为空时使用默认路径 ~/.local/share/opencode/octl.sock。
// method 与 wire 协议一致："snapshot" | "listSessions" | "messages"（messages
// 需提供 sessionID）。timeout 覆盖连接、写入与等待响应的全过程。
// 返回 daemon 应答；Ok=false 时由调用方检查其中的 Error 字段。
func QueryOnce(socketPath, method, sessionID string, timeout time.Duration) (*ResponseMsg, error) {
	return doQueryOnce(socketPath, RequestMsg{Type: "request", Method: method, SessionID: sessionID}, timeout)
}

// QueryDaily 是 "daily" 方法的 QueryOnce 变体：携带 [from, to) 时间窗口
// （unix 毫秒）请求窗口内的活动聚合。
func QueryDaily(socketPath string, from, to int64, timeout time.Duration) (*ResponseMsg, error) {
	return doQueryOnce(socketPath, RequestMsg{Type: "request", Method: "daily", From: from, To: to}, timeout)
}

// QueryReport 是 "report" 方法的一次性封装：请求 daemon 生成 [from, to)
// 窗口的底片并落盘。dirOverride 为空时 daemon 使用默认目录。
func QueryReport(socketPath string, from, to int64, dirOverride string, timeout time.Duration) (*ResponseMsg, error) {
	return doQueryOnce(socketPath, RequestMsg{Type: "request", Method: "report", From: from, To: to, Dir: dirOverride}, timeout)
}

// doQueryOnce 是 QueryOnce / QueryDaily 的公共核心：订阅握手 → 发送请求 →
// 按 ID 匹配收 response。
func doQueryOnce(socketPath string, req RequestMsg, timeout time.Duration) (*ResponseMsg, error) {
	conn, r, err := dialSubscribed(socketPath, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	id := fmt.Sprintf("cli-%d", time.Now().UnixNano())
	req.ID = id
	if err := enc.Encode(&req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var base BaseMsg
			if json.Unmarshal(line, &base) == nil && base.Type == "response" {
				var resp ResponseMsg
				if jerr := json.Unmarshal(line, &resp); jerr == nil {
					// 正常按 ID 关联本次请求；版本不匹配时 daemon 会返回
					// 不带 ID 的错误 response，一并透传给调用方。
					if resp.ID == id || (resp.ID == "" && !resp.Ok) {
						return &resp, nil
					}
				}
			}
		}
		if err != nil {
			if os.IsTimeout(err) {
				return nil, fmt.Errorf("%w after %s", ErrQueryTimeout, timeout)
			}
			return nil, fmt.Errorf("read response: %w", err)
		}
	}
}
