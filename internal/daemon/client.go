package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/tomasWade/octl/internal/plugins"
)

// SocketClient is a Unix socket client that connects to an octl daemon,
// subscribes to snapshot updates, and delivers parsed messages on a channel.
type SocketClient struct {
	socketPath string
	conn       net.Conn
	enc        *json.Encoder
	mu         sync.Mutex
	msgs       chan interface{}
}

// NewSocketClient creates a client for the default daemon socket path.
func NewSocketClient() *SocketClient {
	return NewSocketClientWithSocket("")
}

// NewSocketClientWithSocket creates a client for a custom socket path.
// An empty path falls back to the default ~/.local/share/opencode/octl.sock.
func NewSocketClientWithSocket(socketPath string) *SocketClient {
	return &SocketClient{
		socketPath: socketPath,
		msgs:       make(chan interface{}, 256),
	}
}

// SocketPath returns the resolved Unix socket path.
func (c *SocketClient) SocketPath() (string, error) {
	if c.socketPath != "" {
		return c.socketPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, socketDirPath, socketFileName), nil
}

// Msgs returns the channel of parsed daemon messages. The channel is closed
// when the underlying connection is closed.
func (c *SocketClient) Msgs() <-chan interface{} {
	return c.msgs
}

// Connect dials the daemon Unix socket. It returns an error if the socket is
// missing or the daemon is not accepting connections.
func (c *SocketClient) Connect() error {
	sockPath, err := c.SocketPath()
	if err != nil {
		return err
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("connect to daemon at %s: %w", sockPath, err)
	}

	c.mu.Lock()
	c.conn = conn
	c.enc = json.NewEncoder(conn)
	c.mu.Unlock()

	go c.readLoop(conn)
	return nil
}

// Close terminates the connection. It is safe to call multiple times.
func (c *SocketClient) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()

	if conn != nil {
		return conn.Close()
	}
	return nil
}

// Subscribe sends a subscribe message for the snapshot channel.
func (c *SocketClient) Subscribe() error {
	return c.write(SubscribeMsg{
		Type:     "subscribe",
		Channels: []string{"snapshot"},
		Version:  plugins.ProtocolMD5(),
	})
}

// SubscribeView sends a subscribe message for the view channel.
func (c *SocketClient) SubscribeView() error {
	return c.write(SubscribeMsg{
		Type:     "subscribe",
		Channels: []string{"view"},
		Version:  plugins.ProtocolMD5(),
	})
}

// RequestSnapshot sends a request for a one-time snapshot update.
func (c *SocketClient) RequestSnapshot(id string) error {
	return c.write(RequestMsg{
		Type:   "request",
		Method: "snapshot",
		ID:     id,
	})
}

// RequestListSessions sends a request for the sidebar session list.
func (c *SocketClient) RequestListSessions(id string) error {
	return c.write(RequestMsg{
		Type:   "request",
		Method: "listSessions",
		ID:     id,
	})
}

// RequestMessages sends a request for the text parts of a session.
func (c *SocketClient) RequestMessages(id, sessionID string) error {
	return c.write(RequestMsg{
		Type:      "request",
		Method:    "messages",
		ID:        id,
		SessionID: sessionID,
	})
}

// SendAction sends a management action request to the daemon.
func (c *SocketClient) SendAction(action ActionMsg) error {
	return c.write(action)
}

// Ping sends a keep-alive ping.
func (c *SocketClient) Ping() error {
	return c.write(PingMsg{Type: "ping"})
}

func (c *SocketClient) write(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enc == nil {
		return fmt.Errorf("socket client not connected")
	}
	return c.enc.Encode(v)
}

// readLoop reads JSON lines from the connection and parses them into message
// structs. It closes c.msgs when the connection ends.
// The connection is owned and closed by Close() — readLoop only reads.
func (c *SocketClient) readLoop(conn net.Conn) {
	defer close(c.msgs)

	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			msg, parseErr := parseClientMsg(line)
			if parseErr == nil {
				select {
				case c.msgs <- msg:
				default:
					log.Printf("[client] message dropped (channel full): %T", msg)
				}
			} else {
				log.Printf("[client] parse error: %v | line: %s", parseErr, string(line))
			}
		}
		if err != nil {
			return
		}
	}
}

// parseClientMsg decodes a daemon-to-client message.
func parseClientMsg(line []byte) (interface{}, error) {
	var base BaseMsg
	if err := json.Unmarshal(line, &base); err != nil {
		return nil, err
	}

	switch base.Type {
	case "subscribed":
		var msg SubscribedMsg
		err := json.Unmarshal(line, &msg)
		return msg, err
	case "snapshot":
		var msg SnapshotMsg
		err := json.Unmarshal(line, &msg)
		return msg, err
	case "view":
		var msg ViewMsg
		err := json.Unmarshal(line, &msg)
		return msg, err
	case "pong":
		return PongMsg{Type: "pong"}, nil
	case "response":
		var msg ResponseMsg
		err := json.Unmarshal(line, &msg)
		return msg, err
	case "progress":
		var msg ProgressMsg
		err := json.Unmarshal(line, &msg)
		return msg, err
	case "result":
		var msg ResultMsg
		err := json.Unmarshal(line, &msg)
		return msg, err
	default:
		return base, nil
	}
}
