package main

// ws.go：/ws/fleet WebSocket 集线器。
// 帧格式：{"type":"state","data":FleetSnap}（1Hz + 连接即发）与
// {"type":"event","data":EventSnap}（即时）。客户端断开时读写 goroutine
// 自行退出；写队列满的慢客户端主动断开，防止内存无限增长。

import (
	"encoding/json"
	"log"
	"net/http"
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
	// 开发期放宽跨域；生产由入口层（nginx 同源）收敛。
	CheckOrigin: func(r *http.Request) bool { return true },
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
		select {
		case c.send <- b:
		default:
			delete(h.clients, c)
			close(c.send)
			log.Printf("WS 慢客户端断开: %s", c.conn.RemoteAddr())
		}
	}
}

// ServeWS 返回 /ws/fleet 的 handler。
func (h *Hub) ServeWS(st *State) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS 升级失败: %v", err)
			return
		}
		c := &wsClient{hub: h, conn: conn, send: make(chan []byte, wsWriteBuffer)}
		h.mu.Lock()
		h.clients[c] = struct{}{}
		h.mu.Unlock()
		log.Printf("WS 客户端接入: %s", conn.RemoteAddr())

		// 连上即发一次全量，前端不必等下一个 1Hz 周期。
		if b, err := json.Marshal(map[string]any{"type": "state", "data": st.Snapshot()}); err == nil {
			c.send <- b
		}
		go c.writePump()
		go c.readPump()
	}
}

type wsClient struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
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
	}
}
