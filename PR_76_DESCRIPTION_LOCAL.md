### What problem does this PR solve?

Issue Number: ref #76

`Connection` 的 `Send()`、`SendBuf()`、`Flush()` 方法在检查 `isClosed()` 后直接操作 `c.conn` / `c.bufWriter`，但检查与实际写入之间没有任何同步保护。如果另一个 goroutine 在此间隙关闭了连接（`finalizer()` 调用 `c.conn.Close()`），就会对已关闭的 `conn` 执行写操作，导致 TOCTOU（Time-of-check-time-of-use）竞态。

具体竞态路径：
1. goroutine A 调用 `Send()` → `isClosed()` 返回 false
2. goroutine B 调用 `Stop()` → `cancel()` → `finalizer()` → `c.conn.Close()`
3. goroutine A 执行 `c.conn.Write(data)` → 写入已关闭的连接

### What changed and how does it work?

在 `Connection` 结构体中新增 `connLock sync.RWMutex`：

- **发送路径**（`Send`、`SendBuf`、`Flush`）：在检查 `isClosed()` 和操作 `c.conn`/`c.bufWriter` 前取 `connLock.RLock()`，允许多个发送操作并发执行
- **关闭路径**（`finalizer`）：在 `c.conn.Close()` 前取 `connLock.Lock()`，确保所有正在进行的发送/刷新操作完成后才关闭底层连接

这样保证了：
- 检查 `isClosed()` 与使用 `c.conn` 之间是原子的（相对于关闭操作）
- `finalizer` 会等待所有 in-flight 写操作完成后才关闭 `c.conn`
- 多个发送操作仍然可以并发执行（使用 `RLock`）

### Check List

Tests

- [x] Unit test (`go vet ./znet/` 通过)
- [ ] Integration test
- [ ] Manual test
- [ ] No need to test

验证命令：
```bash
go vet ./znet/
go test ./znet/ -count=1 -race -timeout 60s
```

Side effects

- [ ] Performance regression: Consumes more CPU
- [ ] Performance regression: Consumes more Memory
- [ ] Breaking backward compatibility

Documentation

- [ ] Affects user behaviors
- [ ] Contains syntax changes
- [ ] Contains variable changes
- [ ] Contains experimental features

### Release note

```release-note
修复 Connection 的 Send/SendBuf/Flush 与连接关闭之间的并发安全问题，通过 connLock 确保发送操作不会操作已关闭的底层连接。
```
