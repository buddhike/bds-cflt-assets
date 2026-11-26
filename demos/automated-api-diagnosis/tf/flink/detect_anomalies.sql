INSERT INTO api_unhealthy_events (
  api_id,
  detected_at,
  http_status,
  status_count,
  total_count,
  unhealthy_count,
  unhealthy_rate,
  forecast_value,
  upper_bound,
  lower_bound,
  is_anomaly
)

WITH api_health_windows AS (
  SELECT
    window_start,
    window_end,
    window_time,
    api_name AS api_id,

    COUNT(*) AS total_count,

    SUM(
      CASE
        WHEN CAST(`status` AS INT) >= 400 THEN 1
        ELSE 0
      END
    ) AS unhealthy_count

  FROM TABLE(
    TUMBLE(
      TABLE `gateway-logs`,
      DESCRIPTOR(`timestamp`),
      INTERVAL '10' SECOND
    )
  )
  GROUP BY
    window_start,
    window_end,
    window_time,
    api_name
),

scored_health AS (
  SELECT
    *,
    CAST(unhealthy_count AS DOUBLE)
      / NULLIF(CAST(total_count AS DOUBLE), 0.0) AS unhealthy_rate,

    ML_DETECT_ANOMALIES_ROBUST(
      CAST(unhealthy_count AS DOUBLE)
        / NULLIF(CAST(total_count AS DOUBLE), 0.0),
      window_time,
      JSON_OBJECT(
        'window' VALUE 30,
        'threshold' VALUE 3.0
      )
    ) OVER (
      PARTITION BY api_id
      ORDER BY window_time
      RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
    ) AS anomaly

  FROM api_health_windows
),

scored_health_with_lag AS (
  SELECT
    *,
    LAG(anomaly.is_anomaly) OVER (
      PARTITION BY api_id
      ORDER BY window_time
    ) AS previous_is_anomaly
  FROM scored_health
),

status_windows AS (
  SELECT
    window_start,
    window_end,
    window_time,
    api_name AS api_id,
    CAST(`status` AS STRING) AS http_status,
    COUNT(*) AS status_count

  FROM TABLE(
    TUMBLE(
      TABLE `gateway-logs`,
      DESCRIPTOR(`timestamp`),
      INTERVAL '10' SECOND
    )
  )
  WHERE CAST(`status` AS INT) >= 400
  GROUP BY
    window_start,
    window_end,
    window_time,
    api_name,
    `status`
)

SELECT
  h.api_id,
  h.window_end AS detected_at,
  COALESCE(s.http_status, 'RECOVERED') AS http_status,
  COALESCE(s.status_count, h.unhealthy_count) AS status_count,
  h.total_count,
  h.unhealthy_count,
  h.unhealthy_rate,
  h.anomaly.forecast_value,
  h.anomaly.upper_bound,
  h.anomaly.lower_bound,
  h.anomaly.is_anomaly

FROM scored_health_with_lag AS h
LEFT JOIN status_windows AS s
  ON h.api_id = s.api_id
 AND s.window_time > h.window_time - INTERVAL '10' SECOND
 AND s.window_time <= h.window_time

-- Onset (rising edge: healthy -> anomalous) OR recovery (falling edge:
-- anomalous -> healthy). A recovery window has no rows in status_windows
-- (that is why it recovered), so the LEFT JOIN is required: an INNER JOIN would
-- drop every recovery row instead of emitting it with the COALESCE'd sentinels.
WHERE (h.anomaly.is_anomaly = TRUE  AND COALESCE(h.previous_is_anomaly, FALSE) = FALSE)
   OR (h.anomaly.is_anomaly = FALSE AND COALESCE(h.previous_is_anomaly, FALSE) = TRUE)
