package main

// durable_authority.go：PostgreSQL 真相源的控制权服务。
//
// 它替代原型 UDP/in-memory Authority：租约、fencing、续租幂等和撤销均在
// 可串行化事务中完成。签名后的 LeaseGrant 经绑定 Gateway 的 MQTT topic 下发；
// MQTT/Gateway 只做投递，车端 Safety Arbiter 才是最终执行裁决者。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

const (
	defaultLeaseTTL   = 45 * time.Second
	defaultLeaseGrace = 15 * time.Second
)

var (
	ErrAuthorityUnavailable = errors.New("商用 Control Authority 未就绪")
	ErrLeaseBusy            = errors.New("车辆已有有效控制租约")
	ErrLeaseExpired         = errors.New("租约已经过期，必须重新申请接管")
)

type ControlLease struct {
	LeaseID    string    `json:"lease_id"`
	VehicleID  string    `json:"vehicle_id"`
	GatewayID  string    `json:"gateway_id"`
	DriverID   string    `json:"driver_id"`
	DeviceID   string    `json:"device_id"`
	Fencing    int64     `json:"fencing_token"`
	IssuedAt   time.Time `json:"issued_at"`
	ValidUntil time.Time `json:"valid_until"`
}

type leaseSigner struct {
	private ed25519.PrivateKey
	keyID   string
}

func newLeaseSigner(private ed25519.PrivateKey) (*leaseSigner, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Authority Ed25519 私钥长度无效")
	}
	pub := private.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(pub)
	return &leaseSigner{private: private, keyID: hex.EncodeToString(digest[:8])}, nil
}

// loadLeaseSigner only accepts a 0600 base64 encoded Ed25519 seed/private key.
// 生产环境应由 HSM/KMS 适配器实现同一 sign 接口，文件私钥仅用于集成测试。
func loadLeaseSigner(path string) (*leaseSigner, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrAuthorityUnavailable
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("Authority 私钥权限必须为 0600：%s", path)
	}
	rawText, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(rawText)))
	if err != nil {
		return nil, fmt.Errorf("Authority 私钥必须是 base64 Ed25519 seed/private key: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return newLeaseSigner(ed25519.NewKeyFromSeed(raw))
	case ed25519.PrivateKeySize:
		return newLeaseSigner(ed25519.PrivateKey(raw))
	default:
		return nil, fmt.Errorf("Authority 私钥长度无效")
	}
}

// loadSymmetricKey loads a base64 encoded HMAC key from a root-readable file
// that is explicitly protected as 0600. Missing keys must never silently turn
// the authenticated vehicle data plane into an unauthenticated one.
func loadSymmetricKey(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("Envelope HMAC 密钥路径不能为空")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("Envelope HMAC 密钥权限必须为 0600：%s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) < 32 {
		return nil, fmt.Errorf("Envelope HMAC 密钥必须是至少 256 位 base64 数据")
	}
	return key, nil
}

type DurableAuthority struct {
	db              *sql.DB
	st              *State
	signer          *leaseSigner
	envelopeAuthKey []byte
	leaseTTL        time.Duration
	grace           time.Duration
}

func NewDurableAuthority(db *sql.DB, st *State, signer *leaseSigner, envelopeAuthKey []byte, ttl, grace time.Duration) *DurableAuthority {
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}
	if grace <= 0 {
		grace = defaultLeaseGrace
	}
	return &DurableAuthority{db: db, st: st, signer: signer, envelopeAuthKey: append([]byte(nil), envelopeAuthKey...), leaseTTL: ttl, grace: grace}
}

func (a *DurableAuthority) ready() error {
	if a == nil || a.db == nil || a.signer == nil || a.st == nil || len(a.envelopeAuthKey) < 32 {
		return ErrAuthorityUnavailable
	}
	return nil
}

func randomID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// Request creates a new logical lease. It never preempts another operator:
// takeover replacement is a separate reviewed revoke+grant workflow.
func (a *DurableAuthority) Request(ctx context.Context, driverID, deviceID, vehicleID string) (*ControlLease, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	driverID, deviceID, vehicleID = strings.TrimSpace(driverID), strings.TrimSpace(deviceID), strings.TrimSpace(vehicleID)
	if driverID == "" || deviceID == "" || vehicleID == "" {
		return nil, fmt.Errorf("driver、device 与 vehicle 必填")
	}

	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var gatewayID string
	err = tx.QueryRowContext(ctx, `SELECT gateway_id FROM vehicle_gateways
		WHERE vehicle_id=$1 AND status='active' AND certificate_not_after > now()
		ORDER BY activated_at DESC LIMIT 1 FOR UPDATE`, vehicleID).Scan(&gatewayID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("车辆没有可用的已激活 Gateway")
	}
	if err != nil {
		return nil, err
	}
	if err := requireVehicleLiveForControlTx(ctx, tx, vehicleID); err != nil {
		return nil, err
	}
	if err := requireControlCapabilityTx(ctx, tx, vehicleID, gatewayID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vehicle_control_epochs(vehicle_id) VALUES ($1) ON CONFLICT DO NOTHING`, vehicleID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE control_leases SET state='expired', release_reason='expired'
		WHERE vehicle_id=$1 AND state='active' AND valid_until <= now()`, vehicleID); err != nil {
		return nil, err
	}
	var fencing int64
	if err := tx.QueryRowContext(ctx, `SELECT fencing_token FROM vehicle_control_epochs WHERE vehicle_id=$1 FOR UPDATE`, vehicleID).Scan(&fencing); err != nil {
		return nil, err
	}
	var active string
	err = tx.QueryRowContext(ctx, `SELECT id FROM control_leases WHERE vehicle_id=$1 AND state='active' AND valid_until > now() FOR UPDATE`, vehicleID).Scan(&active)
	if err == nil {
		return nil, ErrLeaseBusy
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var otherVehicle string
	err = tx.QueryRowContext(ctx, `SELECT vehicle_id FROM control_leases WHERE driver_id=$1 AND state='active' AND valid_until > now() FOR UPDATE`, driverID).Scan(&otherVehicle)
	if err == nil && otherVehicle != vehicleID {
		return nil, fmt.Errorf("操作员当前已接管 %s", otherVehicle)
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	leaseID, err := randomID("lease-")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	lease := &ControlLease{LeaseID: leaseID, VehicleID: vehicleID, GatewayID: gatewayID, DriverID: driverID, DeviceID: deviceID, Fencing: fencing + 1, IssuedAt: now, ValidUntil: now.Add(a.leaseTTL)}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_leases
		(id, vehicle_id, gateway_id, driver_id, device_id, fencing_token, state, issued_at, valid_until)
		VALUES ($1,$2,$3,$4,$5,$6,'active',$7,$8)`, lease.LeaseID, lease.VehicleID, lease.GatewayID, lease.DriverID, lease.DeviceID, lease.Fencing, lease.IssuedAt, lease.ValidUntil); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vehicle_control_epochs SET fencing_token=$1, active_lease_id=$2, updated_at=now() WHERE vehicle_id=$3`, lease.Fencing, lease.LeaseID, lease.VehicleID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_security_events(vehicle_id, gateway_id, lease_id, actor, event_type, outcome, detail)
		VALUES ($1,$2,$3,$4,'lease_issued','ok',$5::jsonb)`, lease.VehicleID, lease.GatewayID, lease.LeaseID, lease.DriverID,
		mustJSON(map[string]any{"fencing": lease.Fencing, "device_id": lease.DeviceID, "valid_until": lease.ValidUntil.Format(time.RFC3339Nano)})); err != nil {
		return nil, err
	}
	if err := a.publishGrantTx(ctx, tx, lease, "grant"); err != nil {
		return nil, fmt.Errorf("租约下行加入事务失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.st.wakeOutbox()
	a.reflect(lease, true)
	return lease, nil
}

func (a *DurableAuthority) Renew(ctx context.Context, leaseID, driverID, deviceID, requestID string) (*ControlLease, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(requestID)) < 16 {
		return nil, fmt.Errorf("续租 request_id 必须是客户端生成的至少 16 位随机值")
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	lease, err := scanLease(tx.QueryRowContext(ctx, `SELECT id, vehicle_id, gateway_id, driver_id, device_id, fencing_token, issued_at, valid_until
		FROM control_leases WHERE id=$1 FOR UPDATE`, leaseID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("租约不存在")
	}
	if err != nil {
		return nil, err
	}
	if lease.DriverID != driverID || lease.DeviceID != deviceID {
		return nil, fmt.Errorf("租约不属于当前操作员/控制设备")
	}
	if err := requireControlCapabilityTx(ctx, tx, lease.VehicleID, lease.GatewayID); err != nil {
		return nil, err
	}
	if err := requireVehicleLiveForControlTx(ctx, tx, lease.VehicleID); err != nil {
		return nil, err
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM control_leases WHERE id=$1`, leaseID).Scan(&state); err != nil {
		return nil, err
	}
	if state != "active" || time.Now().After(lease.ValidUntil.Add(a.grace)) {
		_, _ = tx.ExecContext(ctx, `UPDATE control_leases SET state='expired', release_reason='renewal_deadline_exceeded' WHERE id=$1 AND state='active'`, leaseID)
		return nil, ErrLeaseExpired
	}
	var savedUntil time.Time
	err = tx.QueryRowContext(ctx, `SELECT valid_until FROM control_lease_renewals WHERE lease_id=$1 AND request_id=$2`, leaseID, requestID).Scan(&savedUntil)
	if err == nil {
		lease.ValidUntil = savedUntil
		if err := a.publishGrantTx(ctx, tx, lease, "grant"); err != nil {
			return nil, fmt.Errorf("幂等续租下行加入事务失败: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		a.st.wakeOutbox()
		a.reflect(lease, true)
		return lease, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	lease.ValidUntil = time.Now().UTC().Add(a.leaseTTL)
	if _, err := tx.ExecContext(ctx, `UPDATE control_leases SET valid_until=$1 WHERE id=$2 AND state='active'`, lease.ValidUntil, leaseID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_lease_renewals(lease_id, request_id, valid_until) VALUES ($1,$2,$3)`, leaseID, requestID, lease.ValidUntil); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_security_events(vehicle_id, gateway_id, lease_id, actor, event_type, outcome, detail)
		VALUES ($1,$2,$3,$4,'lease_renewed','ok',$5::jsonb)`, lease.VehicleID, lease.GatewayID, lease.LeaseID, lease.DriverID,
		mustJSON(map[string]any{"request_id": requestID, "valid_until": lease.ValidUntil.Format(time.RFC3339Nano)})); err != nil {
		return nil, err
	}
	if err := a.publishGrantTx(ctx, tx, lease, "grant"); err != nil {
		return nil, fmt.Errorf("续租下行加入事务失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.st.wakeOutbox()
	a.reflect(lease, true)
	return lease, nil
}

func (a *DurableAuthority) Release(ctx context.Context, leaseID, driverID, deviceID, reason string) error {
	if err := a.ready(); err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	lease, err := scanLease(tx.QueryRowContext(ctx, `SELECT id, vehicle_id, gateway_id, driver_id, device_id, fencing_token, issued_at, valid_until
		FROM control_leases WHERE id=$1 FOR UPDATE`, leaseID))
	if err != nil {
		return fmt.Errorf("读取租约: %w", err)
	}
	if lease.DriverID != driverID || lease.DeviceID != deviceID {
		return fmt.Errorf("租约不属于当前操作员/控制设备")
	}
	var fencing int64
	if err := tx.QueryRowContext(ctx, `SELECT fencing_token FROM vehicle_control_epochs WHERE vehicle_id=$1 FOR UPDATE`, lease.VehicleID).Scan(&fencing); err != nil {
		return err
	}
	fencing++ // release itself is a new fencing epoch, immediately invalidating old commands.
	res, err := tx.ExecContext(ctx, `UPDATE control_leases SET state='released', released_at=now(), release_reason=$1 WHERE id=$2 AND state='active'`, reason, leaseID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("租约已经不是 active 状态，拒绝重复撤销")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vehicle_control_epochs SET fencing_token=$1, active_lease_id=NULL, updated_at=now() WHERE vehicle_id=$2`, fencing, lease.VehicleID); err != nil {
		return err
	}
	lease.Fencing, lease.ValidUntil = fencing, time.Now().UTC().Add(-time.Second)
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_security_events(vehicle_id, gateway_id, lease_id, actor, event_type, outcome, detail)
		VALUES ($1,$2,$3,$4,'lease_released','ok',$5::jsonb)`, lease.VehicleID, lease.GatewayID, lease.LeaseID, driverID,
		mustJSON(map[string]any{"reason": reason, "revocation_fencing": fencing})); err != nil {
		return err
	}
	if err := a.publishGrantTx(ctx, tx, lease, "revoke"); err != nil {
		return fmt.Errorf("撤销下行加入事务失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.st.wakeOutbox()
	a.reflect(lease, false)
	return nil
}

// EmergencyStop is an independent revocation path: it advances fencing and
// delivers a signed revoke to the car. The Arbiter handles revoke as a local
// minimal-risk transition; no browser-originated zero-speed command is trusted.
func (a *DurableAuthority) EmergencyStop(ctx context.Context, vehicleID, actor string) error {
	if err := a.ready(); err != nil {
		return err
	}
	vehicleID = strings.TrimSpace(vehicleID)
	if vehicleID == "" || strings.TrimSpace(actor) == "" {
		return fmt.Errorf("车辆和操作人必填")
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var gatewayID string
	if err := tx.QueryRowContext(ctx, `SELECT gateway_id FROM vehicle_gateways
		WHERE vehicle_id=$1 AND status='active' AND certificate_not_after > now()
		ORDER BY activated_at DESC LIMIT 1 FOR UPDATE`, vehicleID).Scan(&gatewayID); err != nil {
		return fmt.Errorf("急停无法定位已激活 Gateway: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vehicle_control_epochs(vehicle_id) VALUES ($1) ON CONFLICT DO NOTHING`, vehicleID); err != nil {
		return err
	}
	var fencing int64
	if err := tx.QueryRowContext(ctx, `SELECT fencing_token FROM vehicle_control_epochs WHERE vehicle_id=$1 FOR UPDATE`, vehicleID).Scan(&fencing); err != nil {
		return err
	}
	fencing++
	if _, err := tx.ExecContext(ctx, `UPDATE control_leases SET state='revoked', released_at=now(), release_reason='emergency_stop'
		WHERE vehicle_id=$1 AND state='active'`, vehicleID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vehicle_control_epochs SET fencing_token=$1, active_lease_id=NULL, updated_at=now() WHERE vehicle_id=$2`, fencing, vehicleID); err != nil {
		return err
	}
	leaseID, err := randomID("estop-")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_security_events(vehicle_id, gateway_id, lease_id, actor, event_type, outcome, detail)
		VALUES ($1,$2,$3,$4,'emergency_stop','ok',$5::jsonb)`, vehicleID, gatewayID, leaseID, actor,
		mustJSON(map[string]any{"revocation_fencing": fencing})); err != nil {
		return err
	}
	lease := &ControlLease{LeaseID: leaseID, VehicleID: vehicleID, GatewayID: gatewayID, DriverID: actor, DeviceID: "emergency-stop", Fencing: fencing, IssuedAt: time.Now().UTC(), ValidUntil: time.Now().UTC().Add(-time.Second)}
	if err := a.publishGrantTx(ctx, tx, lease, "revoke"); err != nil {
		return fmt.Errorf("急停下行加入事务失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.st.wakeOutbox()
	a.reflect(lease, false)
	return nil
}

func scanLease(row *sql.Row) (*ControlLease, error) {
	lease := &ControlLease{}
	err := row.Scan(&lease.LeaseID, &lease.VehicleID, &lease.GatewayID, &lease.DriverID, &lease.DeviceID, &lease.Fencing, &lease.IssuedAt, &lease.ValidUntil)
	return lease, err
}

func (a *DurableAuthority) publishGrant(lease *ControlLease, action string) error {
	if err := a.ready(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := a.publishGrantTx(ctx, tx, lease, action); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.st.wakeOutbox()
	return nil
}

func (a *DurableAuthority) publishGrantTx(ctx context.Context, tx *sql.Tx, lease *ControlLease, action string) error {
	if lease == nil || (action != "grant" && action != "revoke") {
		return fmt.Errorf("非法 LeaseGrant")
	}
	if err := a.ready(); err != nil {
		return err
	}
	now := time.Now().UTC()
	leaseAction := platformv1.LeaseAction_LEASE_ACTION_GRANT
	if action == "revoke" {
		leaseAction = platformv1.LeaseAction_LEASE_ACTION_REVOKE
	}
	grant := &platformv1.LeaseGrant{Version: 1, Action: leaseAction, VehicleId: lease.VehicleID,
		GatewayId: lease.GatewayID, LeaseId: lease.LeaseID, DriverId: lease.DriverID,
		DeviceId: lease.DeviceID, FencingToken: uint64(lease.Fencing), ValidUntilUnixNs: lease.ValidUntil.UnixNano(),
		IssuedAtUnixNs: now.UnixNano(), AuthorityKeyId: a.signer.keyID}
	unsigned := proto.Clone(grant).(*platformv1.LeaseGrant)
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
	env := &platformv1.Envelope{SchemaMajor: 1, SchemaMinor: 0,
		MessageType: "platform.v1.LeaseGrant", VehicleId: lease.VehicleID,
		GatewayId: lease.GatewayID, SessionId: "authority", Sequence: uint64(lease.Fencing),
		UtcTimeNs: now.UnixNano(), MonotonicTimeNs: platformv1.MonotonicNowNS(), TtlMs: 30000,
		TraceId: lease.LeaseID, Payload: payload}
	if err := platformv1.SignEnvelope(env, a.envelopeAuthKey); err != nil {
		return fmt.Errorf("LeaseGrant Envelope 认证标签生成失败: %w", err)
	}
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(env)
	if err != nil {
		return err
	}
	return a.st.enqueueOutboxTx(ctx, tx, lease.GatewayID, "lease", "control_lease", lease.LeaseID, b)
}

func (a *DurableAuthority) reflect(lease *ControlLease, active bool) {
	if a.st == nil || lease == nil {
		return
	}
	tk := TakeoverSnap{}
	if active {
		tk = TakeoverSnap{Active: true, Driver: lease.DriverID, LeaseID: lease.LeaseID, Fencing: lease.Fencing,
			SecondsLeft: maxFloat(0, time.Until(lease.ValidUntil).Seconds())}
	}
	a.st.mu.Lock()
	a.st.takeover = tk
	a.st.mu.Unlock()
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
