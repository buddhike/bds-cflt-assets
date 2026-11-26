# EmitAfterGap

A Flink process table function (PTF) for Confluent Cloud. It lets through the first row of
each `PARTITION BY` key and drops the following ones until `gapSeconds` of wall-clock time
have passed since it last let one through, so a sustained failure produces one AI report,
not one per 10-second window. `tf/flink/ai_reports.sql` calls it:

```sql
EmitAfterGap(
  input      => TABLE api_anomalies PARTITION BY (api_id, http_status),
  gapSeconds => CAST(300 AS BIGINT),
  uid        => 'emit-after-gap-v1'
)
```

## Build

```sh
./build.sh
```

Needs only Docker. It runs the tests and builds `target/emit-after-gap.jar` in a pinned
Maven image (Java 17, compiling for Java 11, which Confluent Cloud runs), then copies it to
[`../../tf/udf/emit-after-gap.jar`](../../tf/udf/emit-after-gap.jar). **Commit that jar with
your change.** Launchpad's runner has no Java, so Terraform uploads the committed jar
rather than building one. The build is reproducible (fixed timestamp, pinned image), and
`.github/workflows/automated-api-diagnosis-udf.yml` fails when the committed jar is not what
the source builds.

Flink is `provided`: Confluent Cloud supplies it, so the jar holds only this class.

## Deployment

`tf/ai.tf` does it, as part of the demo: `confluent_flink_artifact` uploads the jar, and the
statement from `tf/flink/create_emit_after_gap.sql.tftpl` runs

```sql
CREATE FUNCTION EmitAfterGap AS 'org.example.EmitAfterGap' USING JAR 'confluent-artifact://<artifact id>'
```

Rename the class or package and that file must change too (`contract_test.go` checks). The
function is registered once per demo, so a new jar reaches a running demo only after
teardown and a new launch.
