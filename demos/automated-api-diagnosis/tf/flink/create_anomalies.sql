CREATE TABLE api_anomalies
WITH (
  'changelog.mode' = 'append',
  'kafka.consumer.isolation-level' = 'read-uncommitted'
)
AS
SELECT
  api_id,
  detected_at,
  http_status,
  status_count,
  total_count,
  unhealthy_count,
  unhealthy_rate,
  forecast_value,
  upper_bound,
  lower_bound
FROM api_unhealthy_events
WHERE is_anomaly = TRUE
  AND http_status <> 'RECOVERED'
