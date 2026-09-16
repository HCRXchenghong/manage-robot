package main

// outbox.go：跨重启的 MQTT 下行投递器。
//
// PostgreSQL 是消息发送状态的权威来源。发送器只会把已持久化的、已签名
// Protobuf Envelope 发布到 MQTT；连接恢复后继续投递 pending/sending 记录。
// “delivered”只代表 MQTT broker 接受了 QoS1 发布，不代表车辆已经执行；
// 车辆执行结果必须由 ControlAck 另行确认。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"time"
)

const (
	outboxBatchSize    = 32
	outboxMaxAttempts  = 20
	outboxClaimTimeout = 30 * time.Second
)

type outboxItem struct {
	id            string
	aggregateType string
	aggregateID   string
	gateway       string
	channel       string
	payload       []byte
	attempts      int
}

type outboxExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func outboxID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "out-" + hex.EncodeToString(b)
}

func (s *State) enqueueOutbox(gatewayID, channel string, payload []byte) bool {
	return s.enqueueOutboxFor(gatewayID, channel, "gateway_message", channel, payload)
}

func (s *State) enqueueOutboxFor(gatewayID, channel, aggregateType, aggregateID string, payload []byte) bool {
	if s == nil || s.db == nil || gatewayID == "" || channel == "" || len(payload) == 0 {
		return false
	}
	if aggregateType == "" || aggregateID == "" {
		return false
	}
	digest := sha256.Sum256(payload)
	dedupe := fmt.Sprintf("%s/%s/%s/%s/%s", aggregateType, aggregateID, gatewayID, channel, hex.EncodeToString(digest[:]))
	id := outboxID()
	if id == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := insertOutbox(ctx, s.db, id, dedupe, aggregateType, aggregateID, gatewayID, channel, payload)
	if err != nil {
		log.Printf("[outbox] 入队失败 gateway=%s channel=%s: %v", gatewayID, channel, err)
		return false
	}
	s.wakeOutbox()
	return true
}

// enqueueOutboxTx is the transaction half of the transactional-outbox
// contract. Domain state and its signed downlink must be committed together;
// a caller must not commit the domain transaction unless this succeeds.
// Unlike enqueueOutboxFor it deliberately does not wake the worker before the
// surrounding transaction commits.
func (s *State) enqueueOutboxTx(ctx context.Context, tx *sql.Tx, gatewayID, channel, aggregateType, aggregateID string, payload []byte) error {
	if s == nil || tx == nil || gatewayID == "" || channel == "" || len(payload) == 0 || aggregateType == "" || aggregateID == "" {
		return fmt.Errorf("outbox 事务字段无效")
	}
	digest := sha256.Sum256(payload)
	dedupe := fmt.Sprintf("%s/%s/%s/%s/%s", aggregateType, aggregateID, gatewayID, channel, hex.EncodeToString(digest[:]))
	id := outboxID()
	if id == "" {
		return fmt.Errorf("无法生成 outbox ID")
	}
	return insertOutbox(ctx, tx, id, dedupe, aggregateType, aggregateID, gatewayID, channel, payload)
}

func insertOutbox(ctx context.Context, execer outboxExecer, id, dedupe, aggregateType, aggregateID, gatewayID, channel string, payload []byte) error {
	_, err := execer.ExecContext(ctx, `
		INSERT INTO platform_outbox(id, dedupe_key, aggregate_type, aggregate_id, gateway_id, channel, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (dedupe_key) DO NOTHING`,
		id, dedupe, aggregateType, aggregateID, gatewayID, channel, payload)
	return err
}

func (s *State) wakeOutbox() {
	if s == nil {
		return
	}
	select {
	case s.outboxWake <- struct{}{}:
	default:
	}
}

func (s *State) outboxLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-s.outboxWake:
		}
		s.drainOutbox()
	}
}

func (s *State) drainOutbox() {
	if s == nil || s.db == nil {
		return
	}
	items, err := s.claimOutbox()
	if err != nil {
		log.Printf("[outbox] 领取失败: %v", err)
		return
	}
	for _, item := range items {
		sendErr := s.publishMQTT(item.gateway, item.channel, item.payload)
		if sendErr == nil {
			s.markOutbox(item, true, item.attempts, "")
		} else {
			log.Printf("[outbox] 投递失败 id=%s attempt=%d: %v", item.id, item.attempts, sendErr)
			s.markOutbox(item, false, item.attempts, sendErr.Error())
		}
	}
}

func (s *State) claimOutbox() ([]outboxItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// sending 是进程崩溃恢复态：超过 claim timeout 后重新变成 pending。
	if _, err := tx.ExecContext(ctx, `UPDATE platform_outbox
		SET status='pending', next_attempt_at=now()
		WHERE status='sending' AND next_attempt_at < now() - $1::interval`,
		fmt.Sprintf("%f seconds", outboxClaimTimeout.Seconds())); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, aggregate_type, aggregate_id, gateway_id, channel, payload, attempts
		FROM platform_outbox
		WHERE status='pending' AND next_attempt_at <= now()
		ORDER BY created_at ASC
		LIMIT $1 FOR UPDATE SKIP LOCKED`, outboxBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]outboxItem, 0, outboxBatchSize)
	for rows.Next() {
		var item outboxItem
		if err := rows.Scan(&item.id, &item.aggregateType, &item.aggregateID, &item.gateway, &item.channel, &item.payload, &item.attempts); err != nil {
			return nil, err
		}
		item.attempts++
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE platform_outbox
			SET status='sending', attempts=$1, next_attempt_at=now() + $2::interval
			WHERE id=$3`, item.attempts, fmt.Sprintf("%f seconds", outboxClaimTimeout.Seconds()), item.id); err != nil {
			return nil, err
		}
	}
	return items, tx.Commit()
}

func (s *State) markOutbox(item outboxItem, delivered bool, attempts int, detail string) {
	id := item.id
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if delivered {
		_, err := s.db.ExecContext(ctx, `UPDATE platform_outbox SET status='delivered', sent_at=now(), last_error='' WHERE id=$1`, id)
		if err != nil {
			log.Printf("[outbox] 标记 delivered 失败 id=%s: %v", id, err)
		}
		if err == nil {
			s.onOutboxDelivered(item)
		}
		return
	}
	status := "pending"
	if attempts >= outboxMaxAttempts {
		status = "dead"
	}
	backoff := attempts * attempts
	if backoff > 60 {
		backoff = 60
	}
	_, err := s.db.ExecContext(ctx, `UPDATE platform_outbox
		SET status=$1, last_error=$2, next_attempt_at=now() + $3::interval
		WHERE id=$4`, status, detail, fmt.Sprintf("%d seconds", backoff), id)
	if err != nil {
		log.Printf("[outbox] 标记失败 id=%s: %v", id, err)
	}
}

func (s *State) onOutboxDelivered(item outboxItem) {
	if s == nil {
		return
	}
	// Broker acceptance is intentionally distinct from vehicle execution: only
	// a signed vehicle ACK can establish that a command was applied.
	if item.aggregateID == "" {
		return
	}
	if item.aggregateType == "navigation_route" {
		s.mu.RLock()
		ns := s.navStore
		s.mu.RUnlock()
		if ns != nil {
			ns.markDispatched(item.aggregateID)
		}
		return
	}
	if item.aggregateType == "map_publication" {
		s.mu.RLock()
		ms := s.mapStore
		s.mu.RUnlock()
		if ms != nil {
			ms.markMapPublicationDispatched(item.aggregateID)
		}
	}
}

func (s *State) publishMQTT(gatewayID, channel string, payload []byte) error {
	s.mu.RLock()
	c := s.mqttPub
	s.mu.RUnlock()
	if c == nil || !c.IsConnected() {
		return fmt.Errorf("MQTT 未连接")
	}
	t := c.Publish(fmt.Sprintf("gateway/%s/%s", gatewayID, channel), 1, false, payload)
	if !t.WaitTimeout(2 * time.Second) {
		return fmt.Errorf("MQTT QoS1 发布超时")
	}
	return t.Error()
}
