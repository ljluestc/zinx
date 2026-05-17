### What problem does this PR solve?

Issue Number: ref #64

当服务器宕机时，zinx 客户端没有自动重连机制。用户需要手动实现重连逻辑。

本 PR 在 `Client` 中添加了内置的断线重连支持，当连接失败或断开时，客户端会自动重试连接直到服务器恢复。

### What changed and how does it work?

**新增 `Client` 字段：**
- `autoReconnect bool` — 是否启用自动重连（默认启用）
- `reconnectInterval time.Duration` — 重试间隔（默认 1 秒）
- `maxReconnectAttempts int` — 最大重试次数（0 = 无限）

**新增 `ClientOption`：**
- `WithAutoReconnect(bool)` — 启用/禁用自动重连
- `WithReconnectInterval(time.Duration)` — 设置重试间隔
- `WithMaxReconnectAttempts(int)` — 设置最大重试次数

**新增 `IClient` 接口方法：**
- `SetAutoReconnect(bool)` / `GetAutoReconnect() bool`
- `SetReconnectInterval(time.Duration)` / `GetReconnectInterval() time.Duration`
- `SetMaxReconnectAttempts(int)` / `GetMaxReconnectAttempts() int`

**核心逻辑：**
- 重构 `Restart()` → `run()` 循环，统一处理首次连接失败和运行中连接断开
- `dial()` 提取为独立方法，支持 TCP / TLS / WebSocket
- 增加 `waitConnectionClosed()`，避免连接启动阶段 `Context()` 初始化竞态导致空指针
- 连接断开后通过连接上下文触发重连循环
- 使用 `waitReconnect()` 配合 `ctx.Done()` 实现可取消的等待

**测试：**
- `TestClientReconnectWhenServerRecovers` — 服务器延迟启动，验证客户端等待并成功连接
- `TestClientReconnectAfterConnectionClosed` — 服务器断开后重启，验证客户端自动重连

### Check List

Tests

- [x] Unit test
- [ ] Integration test
- [ ] Manual test
- [ ] No need to test

验证命令：
```bash
go test ./znet -run 'TestClientReconnectWhenServerRecovers|TestClientReconnectAfterConnectionClosed' -count=1
go test ./znet -run TestNonExistent -count=1
```

Side effects

- [ ] Performance regression: Consumes more CPU
- [ ] Performance regression: Consumes more Memory
- [ ] Breaking backward compatibility

自动重连默认启用，但可通过 `WithAutoReconnect(false)` 禁用，保持向后兼容。

Documentation

- [ ] Affects user behaviors
- [ ] Contains syntax changes
- [ ] Contains variable changes
- [ ] Contains experimental features

### Release note

```release-note
为 Client 添加自动断线重连支持。服务器宕机后客户端自动重试连接直到服务器恢复。可通过 WithAutoReconnect、WithReconnectInterval、WithMaxReconnectAttempts 配置。
```
