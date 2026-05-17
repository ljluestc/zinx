### What problem does this PR solve?
Issue Number: ref #69

在 `znet/connection.go` 中，读协程与写协程的退出/清理语义不完全对称：
- 读协程出错会通过 `Stop()` 走完整连接清理路径；
- 写协程在部分场景下直接退出，且发送队列在 `SendToQueue` 的 `ctx.Done()` 分支中被直接关闭，存在不安全关闭时序。

当客户端不再发送数据、服务端主动写出并出现异常时，应该和读侧错误一样，触发统一的连接停止流程，避免 goroutine/资源清理不一致。

### What changed and how does it work?
本 PR 在 `znet/connection.go` 做了以下改动：

1. 新增 writer 专用停止信号通道
- 增加字段：`writerStopChan chan struct{}`。
- 在首次启动 writer 时初始化该通道。

2. `StartWriter` 增加显式退出信号监听
- 在 `select` 中新增 `case <-c.writerStopChan:`，用于连接终止时主动通知写协程退出。

3. 保持写失败与读失败一致的 Stop 语义
- `Flush()` 失败和 `SendBuf()` 失败路径均调用 `c.Stop()`，触发完整连接生命周期收敛。

4. 移除 `SendToQueue` 中不安全的队列关闭
- 删除 `ctx.Done()` 分支里直接 `close(c.msgBuffChan)` 的逻辑，避免发送路径与其他协程产生 close/send 竞态。

5. 在 `finalizer()` 中统一发出 writer 关闭信号
- 若 `writerStopChan != nil`，在连接最终清理阶段关闭该通道，确保写协程可被显式通知退出。

### Why this fix is correct
- 写侧失败现在明确进入 `Stop()`，与读侧错误处理一致。
- writer 的退出不再依赖发送队列被谁、何时关闭，而是通过专用信号通道协调。
- 避免了 `SendToQueue` 在 `ctx.Done()` 路径关闭共享通道带来的潜在 panic/race 风险。

### Tests
执行了针对性验证：

```bash
go test ./znet -run TestServerDeadLock -count=1
go test ./znet -run TestNonExistent -count=1
```

结果：均通过。

### Backward compatibility
- 无对外 API 变更。
- 行为改进集中在连接内部关闭协调与错误收敛路径，兼容现有调用方式。

### Release note
```release-note
修复 TCP Connection 写协程关闭语义：为 StartWriter 增加显式关闭信号通道，并在写失败时统一触发 Stop()，避免发送队列关闭时序导致的不一致清理风险。
```
