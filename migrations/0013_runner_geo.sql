-- +goose Up
-- Runner geolocation for the fleet world map: lat/lng plus a display city and
-- country. geo_source is 'auto' (egress-IP lookup) or 'manual' (operator
-- override); geo_ip records the address the last auto lookup used so a
-- heartbeat from a moved egress IP triggers a fresh resolve.
ALTER TABLE runners
	ADD COLUMN geo_lat double precision,
	ADD COLUMN geo_lng double precision,
	ADD COLUMN geo_city text NOT NULL DEFAULT '',
	ADD COLUMN geo_country text NOT NULL DEFAULT '',
	ADD COLUMN geo_source text NOT NULL DEFAULT '',
	ADD COLUMN geo_ip text NOT NULL DEFAULT '',
	ADD COLUMN geo_updated_at timestamptz;

-- +goose Down
ALTER TABLE runners
	DROP COLUMN geo_updated_at,
	DROP COLUMN geo_ip,
	DROP COLUMN geo_source,
	DROP COLUMN geo_country,
	DROP COLUMN geo_city,
	DROP COLUMN geo_lng,
	DROP COLUMN geo_lat;
