### What problem does this PR solve?
Issue Number: ref #64

当前 `zinx` 客户端在以下场景缺少内置断线重连能力：
- 启动时服务端暂不可用，首次连接失败后客户端直接退出。
- 连接建立后服务端异常中断，客户端不会自动重连。

结果是业务方必须自行实现重连逻辑，且不同项目行为不一致。

本 PR 为 `Client` 增加统一的自动重连能力：连接失败或断开后，客户端可按策略持续重试直到服务端恢复（或达到配置的重试上限）。

### Root cause
- 客户端生命周期是“单次连接尝试”模型，缺少统一重试循环。
- 缺少可配置的重连策略（开关、间隔、重试次数）。
- 重启/重连过程中连接上下文初始化存在竞态窗口，需要安全等待机制。

### What changed and how does it work?
#### 1) Client 增加重连配置字段
- `autoReconnect bool`：是否自动重连（默认开启）
- `reconnectInterval time.Duration`：重试间隔（默认 1s）
- `maxReconnectAttempts int`：最大重试次数（0 表示无限）

#### 2) 新增配置项（ClientOption）
- `WithAutoReconnect(bool)`
- `WithReconnectInterval(time.Duration)`
- `WithMaxReconnectAttempts(int)`

#### 3) 扩展 `IClient` 接口能力
- `SetAutoReconnect(bool)` / `GetAutoReconnect() bool`
- `SetReconnectInterval(time.Duration)` / `GetReconnectInterval() time.Duration`
- `SetMaxReconnectAttempts(int)` / `GetMaxReconnectAttempts() int`

#### 4) 连接生命周期重构
- 将 `Restart()` 逻辑重构为统一 `run()` 循环：
  - 处理首次 `dial` 失败重试
  - 处理连接存活期断线后的重连
  - 根据 `maxReconnectAttempts` 决定停止或继续
- 抽离 `dial()`，统一 TCP/TLS/WebSocket 建连入口
- 引入 `waitReconnect()`：重连等待阶段可被 `ctx.Done()` 打断，停止更及时
- 引入 `waitConnectionClosed()`：安全等待连接上下文，规避启动阶段空上下文竞态

#### 5) 新增回归测试
- `TestClientReconnectWhenServerRecovers`
  - 先让服务端不可用，再恢复，验证客户端可自动连上。
- `TestClientReconnectAfterConnectionClosed`
  - 建立连接后模拟断开，再恢复服务端，验证自动重连。

### Files changed
- `ziface/iclient.go`
- `znet/options.go`
- `znet/client.go`
- `znet/client_reconnect_test.go`

### Compatibility / risk
- 向后兼容：保留可配置行为，用户可通过 `WithAutoReconnect(false)` 关闭自动重连。
- 风险点主要在连接生命周期时序，已通过专门重连测试覆盖关键路径。

### Validation
已执行并通过的验证命令：
```bash
go test ./znet -run 'TestClientReconnectWhenServerRecovers|TestClientReconnectAfterConnectionClosed' -count=1
```

### Branch / commits
- Branch: `private/issue-64-client-auto-reconnect`
- Commits:
  - `e6b9399` feat(znet): add automatic reconnection support to Client
  - `55510d5` fix(client): keep retrying connection and reconnect on disconnect

### Release note
```release-note
为 Client 增加自动断线重连能力：当服务端暂不可用或连接中断后，客户端可按配置自动重试连接。支持重连开关、重连间隔和最大重试次数配置。
```
