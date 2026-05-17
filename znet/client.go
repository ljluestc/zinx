package znet

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/aceld/zinx/zdecoder"
	"github.com/aceld/zinx/ziface"
	"github.com/aceld/zinx/zlog"
	"github.com/aceld/zinx/zpack"
	"github.com/gorilla/websocket"
)

type Client struct {
	sync.WaitGroup
	sync.Mutex
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	// Client Name 客户端的名称
	Name string
	// IP of the target server to connect 目标连接服务器的IP
	Ip string
	// Port of the target server to connect 目标连接服务器的端口
	Port int
	Url  *url.URL // 扩展，连接时带上其他参数
	// Custom headers for WebSocket connection WebSocket连接的自定义头信息
	WsHeader http.Header
	// Client version tcp,websocket,客户端版本 tcp,websocket
	version string
	// Connection instance 连接实例
	conn ziface.IConnection
	// Connection instance 连接实例的锁，保证可见性
	connMux sync.Mutex
	// Hook function called on connection start 该client的连接创建时Hook函数
	onConnStart func(conn ziface.IConnection)
	// Hook function called on connection stop 该client的连接断开时的Hook函数
	onConnStop func(conn ziface.IConnection)
	// Data packet packer 数据报文封包方式
	packet ziface.IDataPack
	// Asynchronous channel for capturing connection close status 异步捕获连接关闭状态
	// exitChan chan struct{}
	// Message management module 消息管理模块
	msgHandler ziface.IMsgHandle
	// Disassembly and assembly decoder for resolving sticky and broken packages
	//断粘包解码器
	decoder ziface.IDecoder
	// Heartbeat checker 心跳检测器
	hc ziface.IHeartbeatChecker
	// Use TLS 使用TLS
	useTLS bool
	// For websocket connections
	dialer *websocket.Dialer
	// Error channel
	errChan chan error
	// Auto reconnect switch
	autoReconnect bool
	// Reconnect interval
	reconnectInterval time.Duration
	// Maximum reconnect attempts, <=0 means unlimited
	maxReconnectAttempts int
}

func NewClient(ip string, port int, opts ...ClientOption) ziface.IClient {

	c := &Client{
		// Default name, can be modified using the WithNameClient Option
		// (默认名称，可以使用WithNameClient的Option修改)
		Name: "ZinxClientTcp",
		Ip:   ip,
		Port: port,

		msgHandler:           newCliMsgHandle(),
		packet:               zpack.Factory().NewPack(ziface.ZinxDataPack), // Default to using Zinx's TLV packet format(默认使用zinx的TLV封包方式)
		decoder:              zdecoder.NewTLVDecoder(),                     // Default to using Zinx's TLV decoder(默认使用zinx的TLV解码器)
		version:              "tcp",
		errChan:              make(chan error, 1),
		autoReconnect:        true,
		reconnectInterval:    time.Second,
		maxReconnectAttempts: 0,
	}

	// Apply Option settings (应用Option设置)
	for _, opt := range opts {
		opt(c)
	}

	return c
}

func NewWsClient(ip string, port int, opts ...ClientOption) ziface.IClient {

	c := &Client{
		// Default name, can be modified using the WithNameClient Option
		// (默认名称，可以使用WithNameClient的Option修改)
		Name: "ZinxClientWs",
		Ip:   ip,
		Port: port,

		msgHandler:           newCliMsgHandle(),
		packet:               zpack.Factory().NewPack(ziface.ZinxDataPack), // Default to using Zinx's TLV packet format(默认使用zinx的TLV封包方式)
		decoder:              zdecoder.NewTLVDecoder(),                     // Default to using Zinx's TLV decoder(默认使用zinx的TLV解码器)
		version:              "websocket",
		dialer:               &websocket.Dialer{},
		errChan:              make(chan error, 1),
		autoReconnect:        true,
		reconnectInterval:    time.Second,
		maxReconnectAttempts: 0,
	}

	// Apply Option settings (应用Option设置)
	for _, opt := range opts {
		opt(c)
	}

	return c
}

func NewTLSClient(ip string, port int, opts ...ClientOption) ziface.IClient {

	c, _ := NewClient(ip, port, opts...).(*Client)

	c.useTLS = true

	return c
}

// notify error unblock
func (c *Client) notifyErr(err error) {
	select {
	case c.errChan <- err:
	default:
	}
}

// Start starts the client, sends requests and establishes a connection.
// (重新启动客户端，发送请求且建立连接)
func (c *Client) Restart() {
	//try to stop and wait until client stoped
	c.Stop()

	//set started flag
	c.Lock()
	if c.started {
		// already started, just return
		c.Unlock()
		return
	}
	c.started = true
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.Add(1)
	c.Unlock()

	zlog.Ins().InfoF("[START] Zinx Client dial RemoteAddr: %s:%d\n", c.Ip, c.Port)
	go func() {
		defer c.Done()
		c.run()
	}()
}

func (c *Client) run() {
	attempts := 0
	for {
		select {
		case <-c.ctx.Done():
			zlog.Ins().InfoF("client exit.")
			c.setConn(nil)
			return
		default:
		}

		connect, err := c.dial(c.ctx)
		if err != nil {
			c.notifyErr(err)
			if !c.shouldReconnect(attempts) {
				c.setConn(nil)
				return
			}
			attempts++
			if !c.waitReconnect() {
				c.setConn(nil)
				return
			}
			continue
		}

		attempts = 0
		c.setConn(connect)

		zlog.Ins().InfoF("[START] Zinx Client LocalAddr: %s, RemoteAddr: %s\n", connect.LocalAddr(), connect.RemoteAddr())
		// HeartBeat detection
		if c.hc != nil {
			// Bind connection and heartbeat detector after connection is successfully established
			// (创建连接成功，绑定连接与心跳检测器)
			c.hc.BindConn(connect)
		}

		// Start connection
		go connect.Start()

		if !c.waitConnectionClosed(connect) {
			connect.Stop()
			c.setConn(nil)
			zlog.Ins().InfoF("client exit.")
			return
		}

		c.setConn(nil)
		if !c.shouldReconnect(attempts) {
			c.notifyErr(errors.New("connection closed"))
			return
		}
		attempts++
		if !c.waitReconnect() {
			return
		}
	}
}

func (c *Client) shouldReconnect(attempts int) bool {
	if !c.autoReconnect {
		return false
	}
	if c.maxReconnectAttempts <= 0 {
		return true
	}
	return attempts < c.maxReconnectAttempts
}

func (c *Client) waitReconnect() bool {
	retryInterval := c.reconnectInterval
	if retryInterval <= 0 {
		retryInterval = time.Second
	}
	timer := time.NewTimer(retryInterval)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Client) waitConnectionClosed(connect ziface.IConnection) bool {
	for {
		connCtx := connect.Context()
		if connCtx != nil {
			select {
			case <-c.ctx.Done():
				return false
			case <-connCtx.Done():
				return true
			}
		}

		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (c *Client) dial(ctx context.Context) (ziface.IConnection, error) {
	switch c.version {
	case "websocket":
		wsAddr := fmt.Sprintf("ws://%s:%d", c.Ip, c.Port)
		if c.Url != nil {
			wsAddr = c.Url.String()
		}

		wsConn, _, err := c.dialer.DialContext(ctx, wsAddr, c.WsHeader)
		if err != nil {
			zlog.Ins().ErrorF("WsClient connect to server failed, err:%v", err)
			return nil, err
		}
		return newWsClientConn(c, wsConn), nil
	default:
		if c.useTLS {
			config := &tls.Config{
				// Skip certificate verification here because the CA certificate of the certificate issuer is not authenticated
				// (这里是跳过证书验证，因为证书签发机构的CA证书是不被认证的)
				InsecureSkipVerify: true,
			}
			d := &tls.Dialer{
				Config: config,
			}
			conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%v:%v", net.ParseIP(c.Ip), c.Port))
			if err != nil {
				zlog.Ins().ErrorF("tls client connect to server failed, err:%v", err)
				return nil, err
			}
			return newClientConn(c, conn), nil
		}

		d := &net.Dialer{}
		conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%v:%v", net.ParseIP(c.Ip), c.Port))
		if err != nil {
			zlog.Ins().ErrorF("client connect to server failed, err:%v", err)
			return nil, err
		}
		return newClientConn(c, conn), nil
	}
}

// Start starts the client, sends requests and establishes a connection.
// (启动客户端，发送请求且建立连接)
func (c *Client) Start() {

	// Add the decoder to the interceptor list (将解码器添加到拦截器)
	if c.decoder != nil {
		c.msgHandler.AddInterceptor(c.decoder)
	}

	c.Restart()
}

// StartHeartBeat starts heartbeat detection with a fixed time interval.
// interval: the time interval between each heartbeat message.
// (启动心跳检测, interval: 每次发送心跳的时间间隔)
func (c *Client) StartHeartBeat(interval time.Duration) {
	checker := NewHeartbeatChecker(interval)

	// Add the heartbeat checker's route to the client's message handler.
	// (添加心跳检测的路由)
	c.AddRouter(checker.MsgID(), checker.Router())

	// Bind the heartbeat checker to the client's connection.
	// (client绑定心跳检测器)
	c.hc = checker
}

// StartHeartBeatWithOption starts heartbeat detection with a custom callback function.
// interval: the time interval between each heartbeat message.
// option: a HeartBeatOption struct that contains the custom callback function and message
// 启动心跳检测(自定义回调)
func (c *Client) StartHeartBeatWithOption(interval time.Duration, option *ziface.HeartBeatOption) {
	// Create a new heartbeat checker with the given interval.
	checker := NewHeartbeatChecker(interval)

	// Set the heartbeat checker's callback function and message ID based on the HeartBeatOption struct.
	if option != nil {
		checker.SetHeartbeatMsgFunc(option.MakeMsg)
		checker.SetOnRemoteNotAlive(option.OnRemoteNotAlive)
		checker.BindRouter(option.HeartBeatMsgID, option.Router)
	}

	// Add the heartbeat checker's route to the client's message handler.
	c.AddRouter(checker.MsgID(), checker.Router())

	// Bind the heartbeat checker to the client's connection.
	c.hc = checker
}

// 保证重复调用Stop不会导致panic
func (c *Client) Stop() {
	c.Lock()
	defer c.Unlock()
	if !c.started {
		return
	}
	c.started = false

	con := c.Conn()
	if con != nil {
		zlog.Ins().InfoF("[STOP] Zinx Client LocalAddr: %s, RemoteAddr: %s\n", con.LocalAddr(), con.RemoteAddr())
		con.Stop()
	}

	// c.exitChan <- struct{}{}
	// close(c.exitChan)
	// close(c.ErrChan)
	if c.cancel != nil {
		c.cancel()
	}
	c.Wait()
}

func (c *Client) AddRouter(msgID uint32, router ziface.IRouter) {
	c.msgHandler.AddRouter(msgID, router)
}

func (c *Client) Conn() ziface.IConnection {
	c.connMux.Lock()
	defer c.connMux.Unlock()
	return c.conn
}

func (c *Client) setConn(con ziface.IConnection) {
	c.connMux.Lock()
	defer c.connMux.Unlock()
	c.conn = con
}

func (c *Client) SetOnConnStart(hookFunc func(ziface.IConnection)) {
	c.onConnStart = hookFunc
}

func (c *Client) SetOnConnStop(hookFunc func(ziface.IConnection)) {
	c.onConnStop = hookFunc
}

func (c *Client) GetOnConnStart() func(ziface.IConnection) {
	return c.onConnStart
}

func (c *Client) GetOnConnStop() func(ziface.IConnection) {
	return c.onConnStop
}

func (c *Client) GetPacket() ziface.IDataPack {
	return c.packet
}

func (c *Client) SetPacket(packet ziface.IDataPack) {
	c.packet = packet
}

func (c *Client) GetMsgHandler() ziface.IMsgHandle {
	return c.msgHandler
}

func (c *Client) AddInterceptor(interceptor ziface.IInterceptor) {
	c.msgHandler.AddInterceptor(interceptor)
}

func (c *Client) SetDecoder(decoder ziface.IDecoder) {
	c.decoder = decoder
}
func (c *Client) GetLengthField() *ziface.LengthField {
	if c.decoder != nil {
		return c.decoder.GetLengthField()
	}
	return nil
}

func (c *Client) GetErrChan() <-chan error {
	return c.errChan
}

func (c *Client) SetName(name string) {
	c.Name = name
}

func (c *Client) GetName() string {
	return c.Name
}

func (c *Client) SetUrl(url *url.URL) {
	c.Url = url
}

func (c *Client) GetUrl() *url.URL {
	return c.Url
}

func (c *Client) SetWsHeader(header http.Header) {
	c.WsHeader = header
}

func (c *Client) GetWsHeader() http.Header {
	return c.WsHeader
}

func (c *Client) SetAutoReconnect(autoReconnect bool) {
	c.autoReconnect = autoReconnect
}

func (c *Client) GetAutoReconnect() bool {
	return c.autoReconnect
}

func (c *Client) SetReconnectInterval(interval time.Duration) {
	c.reconnectInterval = interval
}

func (c *Client) GetReconnectInterval() time.Duration {
	return c.reconnectInterval
}

func (c *Client) SetMaxReconnectAttempts(attempts int) {
	c.maxReconnectAttempts = attempts
}

func (c *Client) GetMaxReconnectAttempts() int {
	return c.maxReconnectAttempts
}
