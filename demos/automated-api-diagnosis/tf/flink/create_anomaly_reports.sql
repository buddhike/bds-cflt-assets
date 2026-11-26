CREATE TABLE api_anomaly_ai_reports (
  api_id STRING NOT NULL,
  http_status STRING NOT NULL,
  detected_at TIMESTAMP_LTZ(3) NOT NULL,
  agent_status STRING NOT NULL,
  report STRING NOT NULL
) WITH (
  'changelog.mode' = 'append',
  'kafka.consumer.isolation-level' = 'read-uncommitted'
)
