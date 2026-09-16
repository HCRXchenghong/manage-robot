-- 0014：地图删除必须原子清理版本清单。
-- 旧版 map_versions 外键没有 ON DELETE CASCADE，导致权威地图删除被拒绝；
-- 版本和对象均属于地图聚合，删除地图时应由数据库级联清理其清单。
ALTER TABLE map_versions DROP CONSTRAINT IF EXISTS map_versions_map_id_fkey;
ALTER TABLE map_versions ADD CONSTRAINT map_versions_map_id_fkey
  FOREIGN KEY (map_id) REFERENCES maps(id) ON DELETE CASCADE;
