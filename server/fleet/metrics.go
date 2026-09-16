package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// RuntimeMetrics is deliberately dependency-free. The Prometheus exposition
// format is stable, while the counters remain safe on MQTT/HTTP goroutines.
// No vehicle IDs, operator IDs, paths with IDs, or payload data are labels.
type RuntimeMetrics struct {
	startedAt               time.Time
	httpRequests            atomic.Uint64
	httpErrors              atomic.Uint64
	httpDurationNanos       atomic.Uint64
	mqttAccepted            atomic.Uint64
	mqttRejected            atomic.Uint64
	mqttDeadLetters         atomic.Uint64
	dbWriteQueueFallbacks   atomic.Uint64
	websocketConnections    atomic.Uint64
	websocketDisconnections atomic.Uint64
}

func newRuntimeMetrics() *RuntimeMetrics {
	return &RuntimeMetrics{startedAt: time.Now().UTC()}
}

func (m *RuntimeMetrics) observeHTTP(status int, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.httpRequests.Add(1)
	if status >= 400 {
		m.httpErrors.Add(1)
	}
	m.httpDurationNanos.Add(uint64(elapsed))
}

func (m *RuntimeMetrics) render(st *State) string {
	if m == nil {
		return ""
	}
	databaseReady, persistentWritesReady, mqttReady := 0, 0, 0
	outboxPending, outboxDead := int64(0), int64(0)
	if st != nil {
		runtime := st.RuntimeStatus()
		if runtime.DatabaseReady {
			databaseReady = 1
		}
		if runtime.PersistentWritesReady {
			persistentWritesReady = 1
		}
		if runtime.MQTTReady {
			mqttReady = 1
		}
		if st.db != nil {
			ctx, cancel := contextWithMetricsTimeout()
			_ = st.db.QueryRowContext(ctx, "SELECT count(*) FILTER (WHERE status='pending' OR status='sending'), count(*) FILTER (WHERE status='dead') FROM platform_outbox").Scan(&outboxPending, &outboxDead)
			cancel()
		}
	}
	connections := int64(0)
	if st != nil && st.hub != nil {
		connections = int64(st.hub.clientCount())
	}
	avgDuration := float64(0)
	if requests := m.httpRequests.Load(); requests > 0 {
		avgDuration = float64(m.httpDurationNanos.Load()) / float64(requests) / float64(time.Second)
	}
	return fmt.Sprintf(`# HELP robot_agent_uptime_seconds Process uptime in seconds.
# TYPE robot_agent_uptime_seconds gauge
robot_agent_uptime_seconds %.3f
# HELP robot_agent_http_requests_total HTTP responses emitted by the service.
# TYPE robot_agent_http_requests_total counter
robot_agent_http_requests_total %d
# HELP robot_agent_http_errors_total HTTP responses with status >= 400.
# TYPE robot_agent_http_errors_total counter
robot_agent_http_errors_total %d
# HELP robot_agent_http_request_duration_seconds Average observed HTTP duration.
# TYPE robot_agent_http_request_duration_seconds gauge
robot_agent_http_request_duration_seconds %.9f
# HELP robot_agent_database_ready PostgreSQL readiness (1=true).
# TYPE robot_agent_database_ready gauge
robot_agent_database_ready %d
# HELP robot_agent_mqtt_ready MQTT readiness (1=true).
# TYPE robot_agent_mqtt_ready gauge
robot_agent_mqtt_ready %d
# HELP robot_agent_persistent_writes_ready PostgreSQL transaction write readiness (1=true).
# TYPE robot_agent_persistent_writes_ready gauge
robot_agent_persistent_writes_ready %d
# HELP robot_agent_mqtt_messages_accepted_total Validated MQTT envelopes accepted for projection.
# TYPE robot_agent_mqtt_messages_accepted_total counter
robot_agent_mqtt_messages_accepted_total %d
# HELP robot_agent_mqtt_messages_rejected_total MQTT envelopes rejected by validation or registry.
# TYPE robot_agent_mqtt_messages_rejected_total counter
robot_agent_mqtt_messages_rejected_total %d
# HELP robot_agent_mqtt_dead_letters_total MQTT rejections persisted as bounded evidence records.
# TYPE robot_agent_mqtt_dead_letters_total counter
robot_agent_mqtt_dead_letters_total %d
# HELP robot_agent_outbox_pending Number of pending or in-flight outbox records.
# TYPE robot_agent_outbox_pending gauge
robot_agent_outbox_pending %d
# HELP robot_agent_outbox_dead Number of outbox records that exhausted retry attempts.
# TYPE robot_agent_outbox_dead gauge
robot_agent_outbox_dead %d
# HELP robot_agent_websocket_connections Current WebSocket clients.
# TYPE robot_agent_websocket_connections gauge
robot_agent_websocket_connections %d
# HELP robot_agent_websocket_connections_total WebSocket connections accepted.
# TYPE robot_agent_websocket_connections_total counter
robot_agent_websocket_connections_total %d
# HELP robot_agent_websocket_disconnections_total WebSocket connections closed.
# TYPE robot_agent_websocket_disconnections_total counter
robot_agent_websocket_disconnections_total %d
# HELP robot_agent_db_write_queue_fallbacks_total DB writes executed synchronously because the async queue was full.
# TYPE robot_agent_db_write_queue_fallbacks_total counter
robot_agent_db_write_queue_fallbacks_total %d
`, time.Since(m.startedAt).Seconds(), m.httpRequests.Load(), m.httpErrors.Load(), avgDuration,
		databaseReady, mqttReady, persistentWritesReady, m.mqttAccepted.Load(), m.mqttRejected.Load(), m.mqttDeadLetters.Load(), outboxPending, outboxDead,
		connections, m.websocketConnections.Load(), m.websocketDisconnections.Load(), m.dbWriteQueueFallbacks.Load())
}

// contextWithMetricsTimeout avoids importing the metrics handler's context
// details into State; the database query is advisory and must be bounded.
func contextWithMetricsTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 300*time.Millisecond)
}

type metricsResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *metricsResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *metricsResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

// Preserve the optional net/http interfaces used by WebSocket upgrades and
// streaming handlers. Instrumentation must not change transport semantics.
func (w *metricsResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *metricsResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying response writer does not support hijacking")
	}
	return h.Hijack()
}

func (w *metricsResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *metricsResponseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func withMetrics(st *State, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st == nil || st.metrics == nil {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		mw := &metricsResponseWriter{ResponseWriter: w}
		next.ServeHTTP(mw, r)
		status := mw.status
		if status == 0 {
			status = http.StatusOK
		}
		st.metrics.observeHTTP(status, time.Since(started))
	})
}

func (s *State) setMetricsToken(token string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.metricsToken = strings.TrimSpace(token)
	s.mu.Unlock()
}

func serveMetrics(st *State, w http.ResponseWriter, r *http.Request) {
	if st == nil || st.metrics == nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	st.mu.RLock()
	token := st.metricsToken
	st.mu.RUnlock()
	if token != "" {
		provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="robot-agent-metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	} else if !loopbackRemote(r.RemoteAddr) {
		http.Error(w, "metrics restricted to loopback; configure ROBOT_AGENT_METRICS_TOKEN", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(st.metrics.render(st)))
}

func loopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
