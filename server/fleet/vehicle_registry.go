package main

// vehicle_registry.go：车辆与 Gateway 的强身份登记。
//
// 安全边界：车辆 ID 和 Gateway ID 只能由已激活的登记记录决定，不能由
// MQTT Topic、Envelope 或启动参数自行声明。首次激活在独立 mTLS bootstrap
// 端口完成；MQTT Broker 则必须按证书主体配置 Topic ACL（见 deploy 文档）。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	enrollmentTTLMax = time.Hour
	enrollmentTTLDef = 15 * time.Minute
)

type gatewayIdentity struct {
	GatewayID        string
	VehicleID        string
	CertificateHash  string
	CertificateSubj  string
	CertificateUntil time.Time
	Status           string
}

type GatewayRegistry struct {
	db *sql.DB
}

func NewGatewayRegistry(db *sql.DB) *GatewayRegistry { return &GatewayRegistry{db: db} }

func spiffeGatewayID(vehicleID, gatewayID string) string {
	return fmt.Sprintf("spiffe://robot-agent/vehicle/%s/gateway/%s", vehicleID, gatewayID)
}

func certificateFingerprint(cert *x509.Certificate) string {
	s := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(s[:])
}

// certificateSPIFFE 要求正式 Gateway 证书含唯一 URI SAN。CN 不作为身份回退，
// 避免同名 CN、通配 CN 或解析差异造成身份混淆。
func certificateSPIFFE(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", fmt.Errorf("缺少客户端证书")
	}
	if len(cert.URIs) != 1 {
		return "", fmt.Errorf("Gateway 证书必须且只能包含一个 URI SAN")
	}
	u := cert.URIs[0]
	if u.Scheme != "spiffe" || u.Host != "robot-agent" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Gateway URI SAN 不符合平台证书规范")
	}
	return u.String(), nil
}

func randomEnrollmentToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashEnrollmentToken(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

// CreateEnrollment creates a one-time bootstrap token. Its plaintext is never
// stored or logged and is returned to the caller exactly once.
func (r *GatewayRegistry) CreateEnrollment(ctx context.Context, vehicleID, gatewayID, actor string, ttl time.Duration) (string, time.Time, error) {
	if r == nil || r.db == nil {
		return "", time.Time{}, fmt.Errorf("车辆注册中心需要 PostgreSQL")
	}
	vehicleID, gatewayID = strings.TrimSpace(vehicleID), strings.TrimSpace(gatewayID)
	if vehicleID == "" || gatewayID == "" || actor == "" {
		return "", time.Time{}, fmt.Errorf("vehicle_id、gateway_id 与创建人必填")
	}
	if ttl <= 0 {
		ttl = enrollmentTTLDef
	}
	if ttl > enrollmentTTLMax {
		return "", time.Time{}, fmt.Errorf("引导凭证有效期不得超过 %s", enrollmentTTLMax)
	}

	token, err := randomEnrollmentToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("生成引导凭证: %w", err)
	}
	expires := time.Now().Add(ttl)
	id, err := randomEnrollmentToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("生成 enrollment ID: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO gateway_enrollments
		(id, vehicle_id, gateway_id, token_sha256, expected_spiffe_id, expires_at, created_by)
		SELECT $1,$2,$3,$4,$5,$6,$7 WHERE EXISTS (SELECT 1 FROM vehicles WHERE id=$2)`,
		id, vehicleID, gatewayID, hashEnrollmentToken(token), spiffeGatewayID(vehicleID, gatewayID), expires, actor)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("创建 Gateway 引导凭证: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", time.Time{}, fmt.Errorf("车辆不存在，不能创建 Gateway 引导凭证")
	}
	_, _ = r.db.ExecContext(ctx, `INSERT INTO control_security_events(vehicle_id, gateway_id, actor, event_type, outcome, detail)
		VALUES ($1,$2,$3,'gateway_enrollment_created','ok',$4::jsonb)`, vehicleID, gatewayID, actor,
		mustJSON(map[string]any{"expires_at": expires.UTC().Format(time.RFC3339)}))
	return token, expires, nil
}

// Activate binds the one-time enrollment to the exact mTLS client certificate.
// The caller has already been authenticated at a RequireAndVerifyClientCert
// TLS listener; token possession alone is deliberately insufficient.
func (r *GatewayRegistry) Activate(ctx context.Context, vehicleID, gatewayID, token string, cert *x509.Certificate) (*gatewayIdentity, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("车辆注册中心需要 PostgreSQL")
	}
	vehicleID, gatewayID, token = strings.TrimSpace(vehicleID), strings.TrimSpace(gatewayID), strings.TrimSpace(token)
	if vehicleID == "" || gatewayID == "" || token == "" {
		return nil, fmt.Errorf("vehicle_id、gateway_id 与 enrollment_token 必填")
	}
	spiffe, err := certificateSPIFFE(cert)
	if err != nil {
		return nil, err
	}
	wantSPIFFE := spiffeGatewayID(vehicleID, gatewayID)
	if subtle.ConstantTimeCompare([]byte(spiffe), []byte(wantSPIFFE)) != 1 {
		return nil, fmt.Errorf("证书身份与车辆/Gateway 不匹配")
	}
	if time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("客户端证书已经过期")
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var tokenHash, expected string
	var expires time.Time
	err = tx.QueryRowContext(ctx, `SELECT token_sha256, expected_spiffe_id, expires_at
		FROM gateway_enrollments WHERE vehicle_id=$1 AND gateway_id=$2 AND consumed_at IS NULL
		ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, vehicleID, gatewayID).Scan(&tokenHash, &expected, &expires)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("不存在可用的 Gateway 引导凭证")
	}
	if err != nil {
		return nil, err
	}
	if time.Now().After(expires) {
		return nil, fmt.Errorf("Gateway 引导凭证已过期")
	}
	if subtle.ConstantTimeCompare([]byte(tokenHash), []byte(hashEnrollmentToken(token))) != 1 || expected != spiffe {
		return nil, fmt.Errorf("Gateway 引导凭证或证书身份不匹配")
	}

	fp := certificateFingerprint(cert)
	if _, err := tx.ExecContext(ctx, `INSERT INTO vehicle_gateways
		(gateway_id, vehicle_id, certificate_sha256, certificate_subject, certificate_not_after, status, activated_at, last_authenticated_at)
		VALUES ($1,$2,$3,$4,$5,'active',now(),now())
		ON CONFLICT (gateway_id) DO UPDATE SET vehicle_id=EXCLUDED.vehicle_id,
		certificate_sha256=EXCLUDED.certificate_sha256, certificate_subject=EXCLUDED.certificate_subject,
		certificate_not_after=EXCLUDED.certificate_not_after, status='active', revoked_at=NULL,
		revoke_reason='', last_authenticated_at=now()`, gatewayID, vehicleID, fp, spiffe, cert.NotAfter); err != nil {
		return nil, fmt.Errorf("绑定 Gateway 证书: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_enrollments SET consumed_at=now(), consumed_certificate_sha256=$1
		WHERE vehicle_id=$2 AND gateway_id=$3 AND consumed_at IS NULL`, fp, vehicleID, gatewayID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vehicles SET gateway_id=$1, lifecycle_state='offline' WHERE id=$2`, gatewayID, vehicleID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_security_events(vehicle_id, gateway_id, event_type, outcome, detail)
		VALUES ($1,$2,'gateway_activated','ok',$3::jsonb)`, vehicleID, gatewayID, mustJSON(map[string]string{"certificate_sha256": fp, "subject": spiffe})); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &gatewayIdentity{GatewayID: gatewayID, VehicleID: vehicleID, CertificateHash: fp, CertificateSubj: spiffe, CertificateUntil: cert.NotAfter, Status: "active"}, nil
}

// ValidateInbound is called after the MQTT broker has authenticated the
// certificate and enforced its ACL. Fleet-hub still verifies the topic and
// Envelope identities against the authoritative registry before processing.
func (r *GatewayRegistry) ValidateInbound(ctx context.Context, vehicleID, gatewayID string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("车辆注册中心不可用，拒绝 MQTT 车辆消息")
	}
	var status string
	var until time.Time
	err := r.db.QueryRowContext(ctx, `SELECT status, certificate_not_after FROM vehicle_gateways
		WHERE vehicle_id=$1 AND gateway_id=$2`, vehicleID, gatewayID).Scan(&status, &until)
	if err == sql.ErrNoRows {
		return fmt.Errorf("未激活的 vehicle/gateway 组合")
	}
	if err != nil {
		return fmt.Errorf("查询 Gateway 注册: %w", err)
	}
	if status != "active" {
		return fmt.Errorf("Gateway 状态为 %s", status)
	}
	if !time.Now().Before(until) {
		return fmt.Errorf("Gateway 证书已过期")
	}
	_, _ = r.db.ExecContext(ctx, `UPDATE vehicle_gateways SET last_authenticated_at=now() WHERE vehicle_id=$1 AND gateway_id=$2`, vehicleID, gatewayID)
	return nil
}

// AcceptInboundSequence atomically records a validated MQTT Envelope. A false
// result means the exact session/sequence has already been projected; callers
// must not apply the payload a second time. Envelope TTL still rejects old
// signed packets, so the ledger is not used as an unbounded replay store.
func (r *GatewayRegistry) AcceptInboundSequence(ctx context.Context, vehicleID, gatewayID, messageType, sessionID string, sequence uint64) (bool, error) {
	if r == nil || r.db == nil || vehicleID == "" || gatewayID == "" || messageType == "" || sessionID == "" || sequence == 0 {
		return false, fmt.Errorf("MQTT 去重字段无效")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("开启 MQTT 去重事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	accepted, err := r.AcceptInboundSequenceTx(ctx, tx, vehicleID, gatewayID, messageType, sessionID, sequence)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("提交 MQTT 去重事务: %w", err)
	}
	return accepted, nil
}

// AcceptInboundSequenceTx is the transaction-aware form used by projections
// that must commit their business fact and deduplication claim atomically.
// Callers own tx and must commit it only after the corresponding projection
// has been written successfully.
func (r *GatewayRegistry) AcceptInboundSequenceTx(ctx context.Context, tx *sql.Tx, vehicleID, gatewayID, messageType, sessionID string, sequence uint64) (bool, error) {
	if r == nil || tx == nil || vehicleID == "" || gatewayID == "" || messageType == "" || sessionID == "" || sequence == 0 {
		return false, fmt.Errorf("MQTT 去重字段无效")
	}
	// The cursor is the ordering gate. ON CONFLICT ... WHERE makes the update
	// atomic across Fleet instances: only a sequence strictly greater than the
	// persisted cursor can advance the session. A late packet therefore cannot
	// overwrite a newer state projection, even when MQTT callbacks run out of
	// order.
	var cursor uint64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO inbound_message_cursors
			(vehicle_id, gateway_id, message_type, session_id, last_sequence, last_received_at)
		VALUES ($1,$2,$3,$4,$5,now())
		ON CONFLICT (vehicle_id, gateway_id, message_type, session_id) DO UPDATE SET
			last_sequence=EXCLUDED.last_sequence, last_received_at=EXCLUDED.last_received_at
		WHERE inbound_message_cursors.last_sequence < EXCLUDED.last_sequence
		RETURNING last_sequence`, vehicleID, gatewayID, messageType, sessionID, sequence).Scan(&cursor)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("更新 MQTT 会话序列游标: %w", err)
	}
	if cursor != sequence {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO inbound_message_dedup(vehicle_id, gateway_id, message_type, session_id, sequence)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		vehicleID, gatewayID, messageType, sessionID, sequence)
	if err != nil {
		return false, fmt.Errorf("写入 MQTT 去重账本: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *GatewayRegistry) Revoke(ctx context.Context, vehicleID, gatewayID, actor, reason string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("车辆注册中心需要 PostgreSQL")
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("吊销 Gateway 必须填写原因")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE vehicle_gateways SET status='revoked', revoked_at=now(), revoke_reason=$1
		WHERE vehicle_id=$2 AND gateway_id=$3 AND status <> 'revoked'`, reason, vehicleID, gatewayID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("未找到可吊销的 Gateway")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vehicles SET lifecycle_state='quarantined' WHERE id=$1`, vehicleID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_security_events(vehicle_id, gateway_id, actor, event_type, outcome, detail)
		VALUES ($1,$2,$3,'gateway_revoked','ok',$4::jsonb)`, vehicleID, gatewayID, actor,
		mustJSON(map[string]string{"reason": reason})); err != nil {
		return err
	}
	return tx.Commit()
}

func registerGatewayRegistryRoutes(mux *http.ServeMux, st *State, svc *Services) {
	// 管理员只会拿到一次性凭证原文；平台只存摘要。凭证需要通过受控
	// 出厂/安装流程交付给 Gateway，绝不写入事件、日志或前端持久化状态。
	mux.HandleFunc("POST /api/vehicles/{id}/gateway-enrollments", func(w http.ResponseWriter, req *http.Request) {
		sess := requireRole(w, req, "super", "group_admin")
		if sess == nil {
			return
		}
		vehicleID := strings.TrimSpace(req.PathValue("id"))
		if vehicleID == "" || !canAccessVehicle(sess, st, vehicleID) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "车辆不存在或无权操作"})
			return
		}
		body := readJSONBody(w, req)
		if body == nil {
			return
		}
		gatewayID, _ := body["gateway_id"].(string)
		gatewayID = strings.TrimSpace(gatewayID)
		minutes := enrollmentTTLDef.Minutes()
		if raw, ok := body["expires_minutes"].(float64); ok {
			minutes = raw
		}
		if minutes < 1 || minutes > enrollmentTTLMax.Minutes() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_minutes 必须在 1 到 60 之间"})
			return
		}
		token, expires, err := svc.registry.CreateEnrollment(req.Context(), vehicleID, gatewayID, sess.Username, time.Duration(minutes*float64(time.Minute)))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, map[string]any{
			"vehicle_id": vehicleID, "gateway_id": gatewayID,
			"enrollment_token":   token,
			"expected_spiffe_id": spiffeGatewayID(vehicleID, gatewayID),
			"expires_at":         expires.UTC().Format(time.RFC3339),
			"delivery_warning":   "凭证仅显示这一次；请通过受控安装流程交付，不要保存到浏览器、工单正文或聊天记录。",
		})
	})

	mux.HandleFunc("POST /api/vehicles/{id}/gateways/{gateway_id}/revoke", func(w http.ResponseWriter, req *http.Request) {
		sess := requireRole(w, req, "super", "group_admin")
		if sess == nil {
			return
		}
		vehicleID, gatewayID := strings.TrimSpace(req.PathValue("id")), strings.TrimSpace(req.PathValue("gateway_id"))
		if vehicleID == "" || gatewayID == "" || !canAccessVehicle(sess, st, vehicleID) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "车辆/Gateway 不存在或无权操作"})
			return
		}
		body := readJSONBody(w, req)
		if body == nil {
			return
		}
		reason, _ := body["reason"].(string)
		if err := svc.registry.Revoke(req.Context(), vehicleID, gatewayID, sess.Username, strings.TrimSpace(reason)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		st.pushEvent("critical", vehicleID, "Gateway 已吊销并隔离车辆："+strings.TrimSpace(reason), "sys")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "vehicle_id": vehicleID, "gateway_id": gatewayID})
	})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ParseGatewaySPIFFE is deliberately exported to support security tests and
// certificate provisioning tooling without duplicating URL validation rules.
func ParseGatewaySPIFFE(raw string) (*url.URL, error) { return url.Parse(raw) }
