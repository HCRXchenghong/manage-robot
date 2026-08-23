package main

// db.go：PostgreSQL 接入 + 幂等迁移（第 10 步任务 5）。
// 要点：
//   - 全部 SQL 走 $1... 参数化，禁止字符串拼接；
//   - schema_migrations 记录版本，重复启动不重复执行；
//   - DB 不可用时由 main 降级为纯内存模式（返回错误即可）。

import (
	"context"
	"database/sql"
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

// RunMigrations 幂等执行 dir 下的 *.sql（按文件名升序）。
// 已执行的版本记录在 schema_migrations，跳过不重跑。
func RunMigrations(db *sql.DB, dir string) error {
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"); err != nil {
		return fmt.Errorf("建 schema_migrations: %w", err)
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
		var done bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", name).Scan(&done); err != nil {
			return fmt.Errorf("查迁移记录 %s: %w", name, err)
		}
		if done {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("读迁移文件 %s: %w", name, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			return fmt.Errorf("执行迁移 %s: %w", name, err)
		}
		if _, err := db.Exec("INSERT INTO schema_migrations(name) VALUES ($1) ON CONFLICT DO NOTHING", name); err != nil {
			return fmt.Errorf("记录迁移 %s: %w", name, err)
		}
	}
	return nil
}
