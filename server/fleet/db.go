package main

// db.go：PostgreSQL 接入 + 幂等迁移（第 10 步任务 5）。
// 要点：
//   - 全部 SQL 走 $1... 参数化，禁止字符串拼接；
//   - schema_migrations 记录版本，重复启动不重复执行；
//   - DB 不可用时由 main 降级为纯内存模式（返回错误即可）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// OpenDB 连接 PostgreSQL 并做 3 秒 ping 验证。
func OpenDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// probePersistentWrite proves that the configured database role can actually
// write, rather than merely accept a TCP connection or a read-only SELECT.
// The probe uses a transaction-local temporary table and always rolls back, so
// it leaves no business row and cannot be confused with a health-event write.
func probePersistentWrite(ctx context.Context, db *sql.DB) bool {
	if db == nil {
		return false
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: false})
	if err != nil {
		return false
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE robot_agent_write_probe(value integer) ON COMMIT DROP"); err != nil {
		return false
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO robot_agent_write_probe(value) VALUES (1)"); err != nil {
		return false
	}
	return true
}

// RunMigrations 幂等执行 dir 下的 *.sql（按文件名升序）。
// 已执行的版本记录在 schema_migrations，跳过不重跑。
func RunMigrations(db *sql.DB, dir string) error {
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"); err != nil {
		return fmt.Errorf("建 schema_migrations: %w", err)
	}
	if _, err := db.Exec("ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("升级 schema_migrations: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读迁移目录 %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("读迁移文件 %s: %w", name, err)
		}
		digest := sha256.Sum256(body)
		checksum := hex.EncodeToString(digest[:])
		var applied, previous string
		if err := db.QueryRow("SELECT COALESCE(applied_at::text, ''), COALESCE(checksum, '') FROM schema_migrations WHERE name=$1", name).Scan(&applied, &previous); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("查迁移记录 %s: %w", name, err)
		}
		if applied != "" {
			if previous == "" {
				// Existing installations predate checksum recording. Record the
				// current file as their explicit baseline; subsequent edits fail
				// closed instead of silently changing an applied migration.
				if _, err := db.Exec("UPDATE schema_migrations SET checksum=$1 WHERE name=$2", checksum, name); err != nil {
					return fmt.Errorf("记录迁移校验和 %s: %w", name, err)
				}
			} else if previous != checksum {
				return fmt.Errorf("迁移文件 %s 的校验和发生变化；请新增迁移，不要修改已执行文件", name)
			}
			continue
		}
		if _, err := db.Exec(string(body)); err != nil {
			return fmt.Errorf("执行迁移 %s: %w", name, err)
		}
		if _, err := db.Exec("INSERT INTO schema_migrations(name, checksum) VALUES ($1,$2) ON CONFLICT DO NOTHING", name, checksum); err != nil {
			return fmt.Errorf("记录迁移 %s: %w", name, err)
		}
	}
	return nil
}
