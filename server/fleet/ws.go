package main

// ws.go：/ws/fleet WebSocket 集线器。
// 帧格式：{"type":"state","data":FleetSnap}（1Hz + 连接即发）与
// {"type":"event","data":EventSnap}（即时）。每个连接在发送前按其会话过滤，
// 不能因 REST 受控而在 WS 泄露其他分组的数据。客户端断开时读写 goroutine
// 自行退出；写队列满的慢客户端主动断开，防止内存无限增长。

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsWriteBuffer = 64               // 每客户端发送队列
	wsWriteWait   = 10 * time.Second // 单次写超时
	wsPongWait    = 60 * time.Second // pong 截止
	wsPingPeriod  = 30 * time.Second // ping 周期
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	// 浏览器的 Cookie 会在 WS 握手中附带；绝不能接受任意 Origin。
	// 仅生产同源，或 fleet-hub 在环回地址时允许 Vite 本地代理跨端口接入。
	CheckOrigin: allowWSOrigin,
}

func isLoopbackHost(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	name = strings.Trim(name, "[]")
	return strings.EqualFold(name, "localhost") || (net.ParseIP(name) != nil && net.ParseIP(name).IsLoopback())
}

func allowWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return isLoopbackHost(r.Host) && isLoopbackHost(u.Host)
}

// Hub 管理全部 WS 客户端。
type Hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

func NewHub() *Hub { return &Hub{clients: map[*wsClient]struct{}{}} }

// Broadcast 非阻塞群发；队列满的客户端视为慢客户端，立即断开。
func (h *Hub) Broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		h.enqueueLocked(c, b)
	}
}

// PublishState 按连接的会话范围序列化全量快照。每位用户都只能收到其有权访问的车辆和事件。
func (h *Hub) PublishState(snap FleetSnap) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if !c.sessionActive() {
			delete(h.clients, c)
			close(c.send)
			if c.metrics != nil {
				c.metrics.websocketDisconnections.Add(1)
			}
			continue
		}
		b, err := json.Marshal(map[string]any{"type": "state", "data": c.filterSnap(snap)})
		if err == nil {
			h.enqueueLocked(c, b)
		}
	}
}

// PublishEvent 只向拥有目标车辆范围的会话发送即时事件；平台级事件仅发给超管。
func (h *Hub) PublishEvent(event EventSnap) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if !c.sessionActive() {
			delete(h.clients, c)
			close(c.send)
			if c.metrics != nil {
				c.metrics.websocketDisconnections.Add(1)
			}
			continue
		}
		if !c.allowEvent(event) {
			continue
		}
		if b, err := json.Marshal(map[string]any{"type": "event", "data": event}); err == nil {
			h.enqueueLocked(c, b)
		}
	}
}

// enqueueLocked sends or disconnects a slow client. Caller must hold h.mu.
func (h *Hub) enqueueLocked(c *wsClient, b []byte) {
	select {
	case c.send <- b:
	default:
		delete(h.clients, c)
		close(c.send)
		if c.metrics != nil {
			c.metrics.websocketDisconnections.Add(1)
		}
		log.Printf("WS 慢客户端断开: %s", c.conn.RemoteAddr())
	}
}

// ServeWS 返回 /ws/fleet 的 handler。连接存活期间每次推送都会重新读取会话，
// 因此登出、改密或管理员调整分组后，不会继续沿用升级连接时的旧权限。
func (h *Hub) ServeWS(st *State, auth *AuthStore, sess *Session) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth == nil || sess == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS 升级失败: %v", err)
			return
		}
		session := func() *Session { return auth.SessionSnapshot(sess.Token) }
		filter := func(snap FleetSnap) FleetSnap {
			current := session()
			if current == nil {
				return FleetSnap{ServerTimeNS: snap.ServerTimeNS, Vehicles: []VehicleSnap{}, Events: []EventSnap{}}
			}
			return filterSnapFor(snap, current)
		}
		allowEvent := func(event EventSnap) bool {
			current := session()
			if current == nil {
				return false
			}
			if current.Role == "super" {
				return true
			}
			return event.VehicleID != "" && canAccessVehicle(current, st, event.VehicleID)
		}
		c := &wsClient{
			hub: h, conn: conn, send: make(chan []byte, wsWriteBuffer),
			filterSnap: filter, allowEvent: allowEvent,
			sessionActive: func() bool { return session() != nil },
			metrics:       st.metrics,
		}
		h.mu.Lock()
		h.clients[c] = struct{}{}
		h.mu.Unlock()
		if st.metrics != nil {
			st.metrics.websocketConnections.Add(1)
		}
		log.Printf("WS 客户端接入: %s", conn.RemoteAddr())

		// 连上即发一次全量，前端不必等下一个 1Hz 周期。
		if b, err := json.Marshal(map[string]any{"type": "state", "data": filter(st.Snapshot())}); err == nil {
			c.send <- b
		}
		go c.writePump()
		go c.readPump()
	}
}

type wsClient struct {
	hub           *Hub
	conn          *websocket.Conn
	send          chan []byte
	filterSnap    func(FleetSnap) FleetSnap
	allowEvent    func(EventSnap) bool
	sessionActive func() bool
	metrics       *RuntimeMetrics
}

// readPump 只负责探测断开（不消费业务上行消息）。
func (c *wsClient) readPump() {
	defer func() {
		c.hub.remove(c)
		c.conn.Close()
	}()
	c.conn.SetReadLimit(64 * 1024)
	c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	for {
		if _, _, err := c.conn.NextReader(); err != nil {
			return
		}
	}
}

func (c *wsClient) writePump() {
	t := time.NewTicker(wsPingPeriod)
	defer func() {
		t.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok { // 队列被 hub 关闭（慢客户端）
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-t.C:
			c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// remove 注销并关闭发送队列（幂等：慢客户端路径可能已先删除）。
func (h *Hub) remove(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
		if c.metrics != nil {
			c.metrics.websocketDisconnections.Add(1)
		}
	}
}

func (h *Hub) clientCount() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// ServeWSTerminal intentionally does not emulate a shell. A real terminal
// requires the car-side workspace agent to establish an authenticated reverse
// channel; exposing its loopback TCP listener directly would be unsafe.
func ServeWSTerminal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("vehicle_id") == "" {
		http.Error(w, "vehicle_id required", http.StatusBadRequest)
		return
	}
	http.Error(w, "workspace agent is not registered for this vehicle", http.StatusServiceUnavailable)
}
