package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ActionOnce 在一条新连接上完成"订阅握手 → action → 收 result"的一次性
// 交互，供 CLI（octl delete / create / fork / send）使用。daemon 把 action
// 的 progress 与 result 都写回发起连接，因此无需常驻读循环，也不依赖
// SocketClient。
//
// socketPath 为空时使用默认路径 ~/.local/share/octl/octl.sock。
// timeout 覆盖连接、写入与等待结果的全过程。create/fork/send 在 daemon 侧
// 后台启动 opencode 进程后立即返回 result；delete 为同步执行，批量删除的
// 耗时与数量成正比，调用方应给足 timeout。
// 返回 daemon 的最终 ResultMsg（含 Summary）；action 本身失败的信息在
// Summary 的 Results 里，由调用方渲染。
func ActionOnce(socketPath string, action ActionMsg, timeout time.Duration) (*ResultMsg, error) {
	conn, r, err := dialSubscribed(socketPath, timeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	action.Type = "action"
	if err := json.NewEncoder(conn).Encode(&action); err != nil {
		return nil, fmt.Errorf("send action: %w", err)
	}

	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var base BaseMsg
			if json.Unmarshal(line, &base) == nil {
				switch base.Type {
				case "result":
					var res ResultMsg
					if jerr := json.Unmarshal(line, &res); jerr == nil {
						return &res, nil
					}
				case "response":
					// 版本不匹配时 daemon 在握手阶段返回不带 ID 的错误
					// response，透传给调用方。
					var resp ResponseMsg
					if jerr := json.Unmarshal(line, &resp); jerr == nil && !resp.Ok && resp.ID == "" {
						return nil, fmt.Errorf("%s", resp.Error)
					}
				}
				// progress / subscribed 等其他消息跳过。
			}
		}
		if err != nil {
			if os.IsTimeout(err) {
				return nil, fmt.Errorf("%w after %s", ErrQueryTimeout, timeout)
			}
			return nil, fmt.Errorf("read result: %w", err)
		}
	}
}
