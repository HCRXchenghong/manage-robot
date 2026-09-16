-- 0003_event_kind.sql：事件分流（告警与事件=车辆链路/车辆故障；系统审计=平台操作）。
-- kind: veh=车辆侧（注册/上下线/链路丢失/模式与故障）；sys=系统侧（登录/账号/接管/地图/任务/开放 API）。
ALTER TABLE events ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'sys';

-- 存量数据回填：按既有文案前缀归类车辆侧事件。
UPDATE events SET kind = 'veh'
WHERE kind = 'sys'
  AND (text LIKE '车辆注册%'
    OR text LIKE '车辆上线%'
    OR text LIKE '链路丢失%'
    OR text LIKE '模式变化%'
    OR text LIKE '进入最小风险状态%'
    OR text LIKE '车辆已停车%'
    OR text LIKE '切换远程驾驶%'
    OR text LIKE '回到自动驾驶%'
    OR text LIKE '紧急停车已触发%');

CREATE INDEX IF NOT EXISTS events_kind_idx ON events(kind);
