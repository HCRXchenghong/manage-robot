-- 0005_vehicle_location.sql: Persist real localization and GPS fields used by
-- the fleet map. `pose_valid` distinguishes an actual (0,0) from no fix.
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS gps_fix BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS gps_lat DOUBLE PRECISION;
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS gps_lon DOUBLE PRECISION;
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS gps_alt DOUBLE PRECISION;
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS pose_valid BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS pose_frame TEXT;
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS pose_x DOUBLE PRECISION;
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS pose_y DOUBLE PRECISION;
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS pose_yaw DOUBLE PRECISION;
