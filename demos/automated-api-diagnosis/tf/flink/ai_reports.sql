INSERT INTO `api_anomaly_ai_reports`
WITH api_anomalies_down_sampled AS (
  SELECT *
    FROM EmitAfterGap(
    input      => TABLE api_anomalies PARTITION BY (api_id, http_status),
    gapSeconds => CAST(300 AS BIGINT),
    uid        => 'emit-after-gap-v1'
  )
),
reports as (
  SELECT
    a.api_id,
    a.http_status,
    a.detected_at,
    b.status,
    b.response
  FROM api_anomalies_down_sampled AS a,
  LATERAL TABLE(
    AI_RUN_AGENT(
        'anomaly_investigation_agent',
        CONCAT(
            'Analyze this API anomaly and provide a summary of findings in infrastructure in AWS used to run the api. api_id=', a.api_id,
            '; status_code=', CAST(a.http_status AS STRING),
            '; event_time=', CAST(a.detected_at AS STRING)
        )
    )
  ) AS b(status, response)
)
SELECT
  api_id,
  http_status,
  detected_at,
  COALESCE(status, 'UNKNOWN'),
  COALESCE(response, 'The agent returned nothing.')
FROM reports
