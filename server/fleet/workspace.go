package main

// workspace.go：远程维护授权与车端工作空间会话。
//
// 浏览器永远不直连车端 shell。这里先持久化审批和会话，再通过
// PostgreSQL outbox 投递带 HMAC + Authority Ed25519 签名的 TerminalGrant。
// 车辆没有真实 workspace-agent 反向通道时，session 保持
// awaiting_vehicle，WSS 入口明确返回不可用，绝不模拟终端。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

const workspaceTTL = 15 * time.Minute

type workspaceApproval struct {
	ID        string `json:"id"`
	VehicleID string `json:"vehicle_id"`
	Requester string `json:"requester"`
	Approver  string `json:"approver,omitempty"`
	Scope     string `json:"scope"`
	Reason    string `json:"reason"`
	State     string `json:"state"`
	ExpiresNS int64  `json:"expires_ns"`
	CreatedNS int64  `json:"created_ns"`
	DecidedNS int64  `json:"decided_ns,omitempty"`
}

type workspaceSession struct {
	ID          string `json:"id"`
	ApprovalID  string `json:"approval_id"`
	VehicleID   string `json:"vehicle_id"`
	GatewayID   string `json:"gateway_id"`
	OperatorID  string `json:"operator_id"`
	DeviceID    string `json:"device_id"`
	State       string `json:"state"`
	CreatedNS   int64  `json:"created_ns"`
	LastActive  int64  `json:"last_activity_ns"`
	ExpiresNS   int64  `json:"expires_ns"`
	ClosedNS    int64  `json:"closed_ns,omitempty"`
	CloseReason string `json:"close_reason,omitempty"`
}

func secureID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

func tokenDigest(token []byte) string {
	sum := sha256.Sum256(token)
	return hex.EncodeToString(sum[:])
}

func scanApproval(row interface{ Scan(...any) error }) (*workspaceApproval, error) {
	a := &workspaceApproval{}
	var exp, created time.Time
	var decided sql.NullTime
	if err := row.Scan(&a.ID, &a.VehicleID, &a.Requester, &a.Approver, &a.Scope,
		&a.Reason, &a.State, &exp, &created, &decided); err != nil {
		return nil, err
	}
	a.ExpiresNS, a.CreatedNS = exp.UnixNano(), created.UnixNano()
	if decided.Valid {
		a.DecidedNS = decided.Time.UnixNano()
	}
	return a, nil
}

func scanWorkspaceSession(row interface{ Scan(...any) error }) (*workspaceSession, error) {
	s := &workspaceSession{}
	var created, active, exp time.Time
	var closed sql.NullTime
	if err := row.Scan(&s.ID, &s.ApprovalID, &s.VehicleID, &s.GatewayID, &s.OperatorID,
		&s.DeviceID, &s.State, &created, &active, &exp, &closed, &s.CloseReason); err != nil {
		return nil, err
	}
	s.CreatedNS, s.LastActive, s.ExpiresNS = created.UnixNano(), active.UnixNano(), exp.UnixNano()
	if closed.Valid {
		s.ClosedNS = closed.Time.UnixNano()
	}
	return s, nil
}

func registerWorkspaceRoutes(mux *http.ServeMux, svc *Services) {
	// 请求审批：普通用户可以发起，但不能自己批准。
	mux.HandleFunc("POST /api/workspace/approvals", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		vehicleID, _ := body["vehicle_id"].(string)
		scope, _ := body["scope"].(string)
		reason, _ := body["reason"].(string)
		vehicleID, scope, reason = strings.TrimSpace(vehicleID), strings.TrimSpace(scope), trimRune(reason, 500)
		if vehicleID == "" || reason == "" || (scope != "terminal" && scope != "diagnostics" && scope != "files" && scope != "gui") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vehicle_id、合法 scope 与 reason 必填"})
			return
		}
		if !canAccessVehicle(sess, svc.st, vehicleID) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "车辆不存在或无权访问"})
			return
		}
		id, err := secureID("wa-")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法生成审批 ID"})
			return
		}
		now := time.Now().UTC()
		exp := now.Add(workspaceTTL)
		if svc.st.db == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "PostgreSQL 未就绪，拒绝创建维护审批"})
			return
		}
		_, err = svc.st.db.ExecContext(r.Context(), `INSERT INTO workspace_approvals
			(id, vehicle_id, requester, scope, reason, expires_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			id, vehicleID, sess.Username, scope, reason, exp)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "审批持久化失败"})
			return
		}
		svc.st.pushEvent("info", vehicleID, "远程维护审批已提交："+id, "sys")
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "approval": workspaceApproval{ID: id, VehicleID: vehicleID, Requester: sess.Username, Scope: scope, Reason: reason, State: "requested", ExpiresNS: exp.UnixNano(), CreatedNS: now.UnixNano()}})
	})

	mux.HandleFunc("GET /api/workspace/approvals", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || svc.st.db == nil {
			writeJSON(w, http.StatusOK, map[string]any{"approvals": []workspaceApproval{}})
			return
		}
		rows, err := svc.st.db.QueryContext(r.Context(), `SELECT id, vehicle_id, requester, approver, scope, reason, state, expires_at, created_at, decided_at
			FROM workspace_approvals WHERE (requester=$1 OR state='requested') ORDER BY created_at DESC LIMIT 200`, sess.Username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "审批查询失败"})
			return
		}
		defer rows.Close()
		out := make([]workspaceApproval, 0)
		for rows.Next() {
			a, err := scanApproval(rows)
			if err == nil && canAccessVehicle(sess, svc.st, a.VehicleID) {
				out = append(out, *a)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"approvals": out})
	})

	mux.HandleFunc("POST /api/workspace/approvals/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil || svc.st.db == nil {
			return
		}
		id := r.PathValue("id")
		var requester, vehicleID, state string
		if err := svc.st.db.QueryRowContext(r.Context(), `SELECT requester, vehicle_id, state FROM workspace_approvals WHERE id=$1`, id).Scan(&requester, &vehicleID, &state); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "审批不存在"})
			return
		}
		if requester == sess.Username {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "审批人不能与申请人相同"})
			return
		}
		if !canAccessVehicle(sess, svc.st, vehicleID) || state != "requested" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "审批状态或车辆权限不允许批准"})
			return
		}
		res, err := svc.st.db.ExecContext(r.Context(), `UPDATE workspace_approvals SET state='approved', approver=$1, decided_at=now()
			WHERE id=$2 AND state='requested' AND expires_at > now()`, sess.Username, id)
		if err != nil || func() bool { n, _ := res.RowsAffected(); return n != 1 }() {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "审批已过期或已被处理"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approval_id": id, "state": "approved"})
	})

	mux.HandleFunc("POST /api/workspace/sessions", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		approvalID, deviceID := strings.TrimSpace(stringOf(body["approval_id"])), strings.TrimSpace(stringOf(body["device_id"]))
		if approvalID == "" || deviceID == "" || svc.st.db == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "approval_id、device_id 必填且数据库必须就绪"})
			return
		}
		device, ok := svc.devices.Get(sess.Username, deviceID)
		if !ok || !device.Online {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "控制设备没有受信在线证明，拒绝创建维护会话"})
			return
		}
		var vehicleID, gatewayID string
		var exp time.Time
		if err := svc.st.db.QueryRowContext(r.Context(), `SELECT a.vehicle_id, COALESCE(v.gateway_id,''), a.expires_at
			FROM workspace_approvals a JOIN vehicles v ON v.id=a.vehicle_id
			WHERE a.id=$1 AND a.requester=$2 AND a.state='approved' AND a.expires_at > now()`, approvalID, sess.Username).Scan(&vehicleID, &gatewayID, &exp); err != nil || gatewayID == "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "审批无效、已过期或车辆没有绑定 Gateway"})
			return
		}
		if !canAccessVehicle(sess, svc.st, vehicleID) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "车辆无权访问"})
			return
		}
		sessionID, err := secureID("ws-")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法生成会话 ID"})
			return
		}
		rawToken := make([]byte, 32)
		if _, err := rand.Read(rawToken); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法生成会话令牌"})
			return
		}
		now := time.Now().UTC()
		until := now.Add(workspaceTTL)
		if until.After(exp) {
			until = exp
		}
		tx, err := svc.st.db.BeginTx(r.Context(), &sql.TxOptions{})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "工作空间会话事务无法开始"})
			return
		}
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(r.Context(), `INSERT INTO workspace_sessions
			(id, approval_id, vehicle_id, gateway_id, operator_id, device_id, token_hash, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, sessionID, approvalID, vehicleID, gatewayID, sess.Username, deviceID, tokenDigest(rawToken), until)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "工作空间会话持久化失败"})
			return
		}
		if err := svc.authority.publishTerminalGrantTx(r.Context(), tx, sessionID, vehicleID, gatewayID, sess.Username, deviceID, rawToken, until); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "车端授权投递未进入可靠 outbox，会话未开放"})
			return
		}
		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "工作空间会话事务提交失败"})
			return
		}
		svc.st.wakeOutbox()
		svc.st.pushEvent("warn", vehicleID, "远程维护会话已授权，等待车端 workspace-agent："+sessionID, "sys")
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "session": workspaceSession{ID: sessionID, ApprovalID: approvalID, VehicleID: vehicleID, GatewayID: gatewayID, OperatorID: sess.Username, DeviceID: deviceID, State: "awaiting_vehicle", CreatedNS: now.UnixNano(), LastActive: now.UnixNano(), ExpiresNS: until.UnixNano()}, "terminal_token": hex.EncodeToString(rawToken), "warning": "令牌只显示一次；车端反向通道建立前不会开放终端"})
	})

	mux.HandleFunc("GET /api/workspace/sessions", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || svc.st.db == nil {
			writeJSON(w, http.StatusOK, map[string]any{"sessions": []workspaceSession{}})
			return
		}
		rows, err := svc.st.db.QueryContext(r.Context(), `SELECT id, approval_id, vehicle_id, gateway_id, operator_id, device_id, state, created_at, last_activity_at, expires_at, closed_at, close_reason
			FROM workspace_sessions WHERE operator_id=$1 ORDER BY created_at DESC LIMIT 100`, sess.Username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "工作空间会话查询失败"})
			return
		}
		defer rows.Close()
		out := make([]workspaceSession, 0)
		for rows.Next() {
			if s, err := scanWorkspaceSession(rows); err == nil {
				out = append(out, *s)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
	})

	mux.HandleFunc("POST /api/workspace/sessions/{id}/close", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || svc.st.db == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或数据库未就绪"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		reason := trimRune(stringOf(body["reason"]), 200)
		if reason == "" {
			reason = "operator_close"
		}
		res, err := svc.st.db.ExecContext(r.Context(), `UPDATE workspace_sessions SET state='closed', closed_at=now(), close_reason=$1
			WHERE id=$2 AND operator_id=$3 AND state IN ('awaiting_vehicle','active')`, reason, r.PathValue("id"), sess.Username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "关闭会话失败"})
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "会话不存在或已关闭"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func (a *DurableAuthority) publishTerminalGrant(sessionID, vehicleID, gatewayID, driverID, deviceID string, token []byte, until time.Time) bool {
	if err := a.ready(); err != nil || sessionID == "" || len(token) < 32 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false
	}
	defer func() { _ = tx.Rollback() }()
	if err := a.publishTerminalGrantTx(ctx, tx, sessionID, vehicleID, gatewayID, driverID, deviceID, token, until); err != nil {
		return false
	}
	if err := tx.Commit(); err != nil {
		return false
	}
	a.st.wakeOutbox()
	return true
}

func (a *DurableAuthority) publishTerminalGrantTx(ctx context.Context, tx *sql.Tx, sessionID, vehicleID, gatewayID, driverID, deviceID string, token []byte, until time.Time) error {
	if err := a.ready(); err != nil || tx == nil || sessionID == "" || vehicleID == "" || gatewayID == "" || len(token) < 32 {
		return fmt.Errorf("维护授权事务字段无效")
	}
	now := time.Now().UTC()
	grant := &platformv1.TerminalGrant{Version: 1, VehicleId: vehicleID, GatewayId: gatewayID,
		SessionId: sessionID, TerminalToken: append([]byte(nil), token...), DriverId: driverID,
		DeviceId: deviceID, ValidUntilUnixNs: until.UnixNano(), IssuedAtUnixNs: now.UnixNano(), AuthorityKeyId: a.signer.keyID}
	unsigned := proto.Clone(grant).(*platformv1.TerminalGrant)
	unsigned.AuthoritySignature = nil
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(unsigned)
	if err != nil {
		return err
	}
	grant.AuthoritySignature = ed25519.Sign(a.signer.private, canonical)
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(grant)
	if err != nil {
		return err
	}
	sequenceBytes := make([]byte, 8)
	if _, err := rand.Read(sequenceBytes); err != nil {
		return err
	}
	var sequence uint64
	for _, b := range sequenceBytes {
		sequence = sequence<<8 | uint64(b)
	}
	env := &platformv1.Envelope{SchemaMajor: 1, SchemaMinor: 0, MessageType: "platform.v1.TerminalGrant",
		VehicleId: vehicleID, GatewayId: gatewayID, SessionId: sessionID, Sequence: sequence,
		UtcTimeNs: now.UnixNano(), MonotonicTimeNs: platformv1.MonotonicNowNS(), TtlMs: 30000,
		TraceId: sessionID, Payload: payload}
	if err := platformv1.SignEnvelope(env, a.envelopeAuthKey); err != nil {
		return err
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(env)
	if err != nil {
		return err
	}
	return a.st.enqueueOutboxTx(ctx, tx, gatewayID, "workspace", "workspace_session", sessionID, encoded)
}
