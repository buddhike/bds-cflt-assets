CREATE TABLE api_unhealthy_events (
  api_id STRING NOT NULL,
  detected_at TIMESTAMP_LTZ(3) NOT NULL,
  http_status STRING NOT NULL,
  status_count BIGINT NOT NULL,
  total_count BIGINT NOT NULL,
  unhealthy_count BIGINT NOT NULL,
  unhealthy_rate DOUBLE NOT NULL,
  forecast_value DOUBLE,
  upper_bound DOUBLE,
  lower_bound DOUBLE,
  is_anomaly BOOLEAN NOT NULL,

  WATERMARK FOR detected_at AS detected_at - INTERVAL '5' SECOND,

  PRIMARY KEY (api_id, detected_at, http_status) NOT ENFORCED
)
WITH (
  'changelog.mode' = 'append',
  'kafka.consumer.isolation-level' = 'read-uncommitted',
  'scan.startup.mode' = 'latest-offset'
)
