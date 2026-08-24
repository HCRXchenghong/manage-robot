-- 0002：地图中心 + 开放 API（等保三级）配套表。

CREATE TABLE IF NOT EXISTS maps (
    id          TEXT PRIMARY KEY,
    vehicle_id  TEXT NOT NULL,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    sha256      TEXT NOT NULL,
    size        BIGINT NOT NULL DEFAULT 0,
    source      TEXT NOT NULL DEFAULT 'manual',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS map_versions (
    map_id      TEXT NOT NULL REFERENCES maps(id),
    version     INT NOT NULL,
    kind        TEXT NOT NULL,
    note        TEXT NOT NULL DEFAULT '',
    author      TEXT NOT NULL DEFAULT '',
    derived_from TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (map_id, version)
);

CREATE TABLE IF NOT EXISTS api_keys (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    prefix      TEXT NOT NULL,
    key_hash    TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);

-- 等保三级「安全审计」：调用审计记录（含失败原因），留存 >= 6 个月由运维策略保证。
CREATE TABLE IF NOT EXISTS audit_log (
    id          BIGSERIAL PRIMARY KEY,
    ts          TIMESTAMPTZ NOT NULL DEFAULT now(),
    key_id      TEXT NOT NULL DEFAULT '',
    key_name    TEXT NOT NULL DEFAULT '',
    method      TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL DEFAULT '',
    result      TEXT NOT NULL DEFAULT '',
    http_code   INT NOT NULL DEFAULT 0,
    ip          TEXT,
    trace_id    TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_log_ts_idx ON audit_log (ts DESC);
