# 业务协议层 codec 设计

> 对应 `src/pkg/codec` 包（待实现）。本文是协议定稿 + 落地方案，写代码前先定此文。

## 1. 分层与归属

| 层 | 内容 | 归属 | 说明 |
|---|---|---|---|
| 传输层 | `LengthHeaderCodec`（4B BE 长度分帧） | `src/pkg/network`（已有，保留） | 通用、与业务无关，任何用本库的项目可复用 |
| 业务协议层 | `MessageCodec`（flag/cmdid/seq/err_code/msgType） | `src/pkg/codec`（新） | GameServerPro 项目自有协议，不进 `network`，保持传输库可复用 |

**为什么不把 `MessageCodec` 放 `network`**：
1. **职责**：`network` 是事件驱动传输库（epoll/Conn/Buffer/Acceptor），对标 gnet/muduo 的传输层。gnet 不带业务 codec；muduo 只把 `LengthHeaderCodec` 当通用示例放 `net`，业务协议留应用层。
2. **可复用**：`network` 将来给别的项目用，不应被 GameServerPro 协议污染。
3. **演进频率**：传输层稳定少动；业务协议（cmdid 编排、err_code 取值、msgType 扩展）随业务演进。分包 = 分开测试、分开演进。
4. **依赖方向**：`codec` import `network`（用 `*Conn`/`*Buffer`/`MessageCallback`），`network` 不反向 import `codec`，无环。

gatesvr/lobbysvr/routeragent 都 `import "project/src/pkg/codec"` 挂同一份 `MessageCodec`，共享协议实现。

## 2. 与 `LengthHeaderCodec` 的关系（方案 B：合一）

两个选择：

- **A（嵌套）**：`network.LengthHeaderCodec` 做外层分帧，`codec.MessageCodec` 做内层解包。两层 codec 串联，略绕。
- **B（合一）**：`codec.MessageCodec` 自己含 4B 长度分帧（直接用 `network.Buffer` 的 `PeekUint32BE/PrependUint32BE` 分帧 API），一个 codec 同时管长度分帧 + 业务解包。

**选 B**：业务侧只挂一个 `codec.MessageCodec`，不与 `LengthHeaderCodec` 串联。`network` 库提供分帧 API（`PeekN`/`PeekUint32BE`/`ReadUint32BE`/`ReadUint16BE`/`PrependUint32BE`）供 `codec` 直接用，`LengthHeaderCodec` 作为"不关心业务协议时的最小分帧 codec"保留。

## 3. 帧格式（终版）

```
+----------------+--------+--------+--------+----------------+
| 4B BE length  | 1B flag| 2B cmdid|4B seq? |     body       |
+----------------+--------+--------+--------+----------------+
```

- `length`：4 字节大端无符号，**= flag + cmdid + (seq) + body 的字节数**（不含自身这 4B）。上限 64MiB（沿用 `network` 现有默认，可配），超限强关连接。
- `flag`：1 字节，见下。
- `cmdid`：2 字节大端无符号，业务命令 ID。
- `seq`：4 字节大端无符号，**仅 request/response 出现**，oneway/控制消息不带。
- `body`：长度由 `length - header` 推得。

各 msgType 的具体字段布局见 §5。

## 4. flag 字节（1B）

| bit | 含义 | 说明 |
|---|---|---|
| 0-3 | msgType（低 4 位） | 0-15，目前用 0-6 |
| 4 | compress | 预留压缩标志 |
| 5 | encrypt | 预留加密标志 |
| 6 | routeDict | 预留路由字典压缩 cmdid |
| 7 | reserved | 兜底 |

**1B 够用论证**：msgType 用 4 位（0-15），现占 7 个；compress/encrypt/routeDict 各 1 位预留；1 位兜底。与 Thrift/TARS/bRPC 同档（均 1B 元信息位）。后续若 4 位 msgType 不够，可用 reserved 位扩成 5-8 位。

## 5. msgType 与字段存在性

| 值 | 类型 | 帧后字段 | 方向 |
|---|---|---|---|
| 0 | handshake | 仅 flag（响应 body 带 server_time+心跳间隔） | C→S / S→C |
| 1 | handshakeAck | 仅 flag | C→S |
| 2 | heartbeat | 仅 flag（body 可带 client_ts 回显测 RTT） | 双向 |
| 3 | kick | 仅 flag（主动踢人） | S→C |
| 4 | request | `[2B cmdid][4B seq][body=req]` | C→S |
| 5 | response | `[2B cmdid][4B seq][body=[2B err_code][result?]]` | S→C（或后端→gate→C） |
| 6 | oneway | `[2B cmdid][body]` | 双向，无 seq 无 err |

### request（msgType=4）
```
[4B len][1B flag=0x04][2B cmdid][4B seq][body=req]
```

### response（msgType=5）
```
[4B len][1B flag=0x05][2B cmdid][4B seq][2B err_code][result?]
```
- `err_code=0`：成功，`result` 即回报 payload。
- `err_code!=0`：失败，无 `result`。
- err_code 内联在 response body 首，**不另设 error msgType**（L2 内联式，Pomelo/TARS 同；不学 Thrift 的 L3 独立 error 消息）。

### oneway（msgType=6）
```
[4B len][1B flag=0x06][2B cmdid][body]
```
- 双向（C→S / S→C 都行），无 seq、无 err_code。
- 典型用途：notify 通知、time-sync 授时下发。

## 6. 关键决策（已敲定，不再回头）

1. **notify + oneway 合并 → 统一叫 `oneway`**：靠方向（C→S / S→C）区分，不在包头上分。无 seq、无 err_code。
2. **err_code 2B 内联在 response body 首**（L2），不另设 error msgType。`err_code=0` 成功带 result；`!=0` 失败无 result。
3. **cmdid 2B**（65536 业务命令；真不够用 routeDict 压缩或留 0xFFFF 扩展）。
4. **seq 4B**（~42 亿并发请求；request/response 必带，oneway/控制消息不带）。
5. **handshake/heartbeat/kick 仅 flag**，无 cmdid/seq/body。
6. **heartbeat 不带服务器时间**：保活为主；RTT 靠 client_ts 回显（可选，body 带即可）。
7. **时钟同步是业务层**：handshake 一次性带 `server_time_ms` 建基线；外加低频 time-sync oneway cmdid 补漂移；Cristian 算法业务侧自己写，库不管。

## 7. 控制消息与时钟同步

### handshake（msgType=0）
- C→S：客户端发起握手，body 可空或带客户端能力位。
- S→C：服务端响应，body 结构：
  ```
  [8B server_time_ms][2B heartbeat_interval_s]
  ```
  - `server_time_ms`：服务端当前毫秒时间戳，客户端据此建时钟基线（Cristian offset 初值）。
  - `heartbeat_interval_s`：服务端定的心跳间隔（默认 30s），客户端据此发 heartbeat。

### heartbeat（msgType=2）
- body 可选带 `8B client_ts`（客户端发心跳时刻的本地毫秒戳），服务端原样回显 → 客户端算 `RTT = T2 - T1`（仅用客户端时钟）。
- 频率：见 §8。

### time-sync oneway（cmdid=约定值，如 0x0001）
- S→C oneway，body = `[8B server_time_ms]`。
- 低频补漂移，见 §8。

## 8. 频率约定

| 消息 | 频率 | 理由 |
|---|---|---|
| heartbeat（保活+RTT 回显） | 15~30s（30s 常见，服务端定、握手告知） | 赶在 NAT 空闲超时（60~300s）前发；顺带刷 RTT，30s 够延迟显示 |
| server time-sync oneway | 60~120s（或仅握手一次） | 时钟漂移慢（~10~100ppm），60s 内 <几 ms；低频省带宽 |
| 纯 RTT 采样 | 1~5s（可选） | 仅当要实时延迟显示且嫌 heartbeat 太粗，否则搭 heartbeat 免 |

带宽自检：100K 连 × 30s × 两向各 ~14B ≈ 95KB/s，可忽略。

反作弊不靠"多下发服务端时间"——靠**服务端自己权威校时**（服务端用自己钟判定，无视客户端报的）；给客户端的时间仅让客户端显示/本地推算对齐，低频够。

## 9. 落地分工

| 侧 | 职责 |
|---|---|
| 库侧（`src/pkg/codec`，我来写） | `MessageCodec`（解 flag→msgType→按需取 cmdid/seq→切 body 派发 `OnMessage(c, msg)`）；handshake 响应带 `server_time_ms`+`heartbeat_interval` 字段位；heartbeat 可带 client_ts 回显 |
| 业务侧（你自己写） | Cristian 时钟同步算法；time-sync cmdid 的下发频率与处理；cmdid 编排；err_code 取值表 |

## 10. 包结构与文件布局

```
src/pkg/codec/
    doc.go          包文档 + 协议常量（msgType/flag 位掩码）
    frame.go        flag/msgType 常量 + flag 编码/解码辅助 + Message 结构
    message.go      MessageCodec：基于 network.Buffer 分帧 API 自管 4B 长度分帧 + 业务解包
    handshake.go    handshake 响应（server_time+heartbeat_interval）字段位编解码
    heartbeat.go    heartbeat 可带 client_ts 回显
    message_test.go 粘包/半包/各 msgType 切分/err_code 路径
```

## 11. API 草图

```go
package codec

// Message 一条已解包的业务消息。
type Message struct {
    MsgType uint8   // 0-6
    CmdID   uint16  // oneway/request/response 有；控制消息为 0
    Seq     uint32  // request/response 有；oneway/控制消息为 0
    ErrCode uint16  // 仅 response 有
    Body    []byte  // payload（response 时含 err_code 之后的 result）
}

// MessageCodec 业务协议 codec，挂在 network.Server/Client 的 OnMessage 上。
type MessageCodec struct {
    OnMessage  func(c *network.Conn, msg *Message)
    OnHandshake func(c *network.Conn, serverTimeMs uint64, heartbeatIntervalS uint16)
    OnHeartbeat func(c *network.Conn, clientTs uint64, hasTs bool)
    OnKick     func(c *network.Conn)
    MaxLen     uint32 // 默认 64MiB
}

// HandleMessage 作为 network.MessageCallback 挂载：从 in *Buffer 切分多帧 → 解包 → 派发。
func (m *MessageCodec) HandleMessage(c *network.Conn, in *network.Buffer)

// EncodeRequest / EncodeResponse / EncodeOneway / EncodeHandshake / EncodeHeartbeat
// 编码并经 c.Send 发出（in-loop 调用）。
```

## 12. 行业参照

- **Thrift**：version+type、seqid、Exception msgType（L3 独立错误消息）。我们选 L2 内联，不学 L3。
- **TARS**：1B flag + iRet（err_code 内联）+ requestId。与我们 err_code 内联思路一致。
- **bRPC baidu_std**：1B meta（is_response/direction/correlation_id_size）。1B 元信息位同档。
- **gRPC**：HTTP/2 传输 + grpc-status 传输层错误。我们是 TCP 自分帧，不走 HTTP/2。
- **Pomelo（NetEase）**：两层（Packet[type|len|body] + Message[type|SeqID|CmdID|err_code|body]）。我们**压成单层**（length 前缀 + flag 即 Packet+Message 的融合），更省一次分帧。

## 13. 待确认

1. 业务协议层另起 `src/pkg/codec` 包（不放 `network`）——**已倾向，待确认**。
2. 方案 B（`MessageCodec` 自管 4B 长度分帧，不与 `LengthHeaderCodec` 串联）——**已倾向，待确认**。
3. time-sync cmdid 取值（如 `0x0001`）——业务侧定，codec 留常量位即可。

确认后即可按 §10 实现 `src/pkg/codec`。
