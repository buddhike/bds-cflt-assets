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

## Build and release

```sh
./build.sh      # test, and build target/emit-after-gap.jar (needs only Docker)
```

It runs the tests and builds the jar in a pinned Maven image (Java 17, compiling for
Java 11, which Confluent Cloud runs). Flink is `provided`: Confluent Cloud supplies it, so
the jar holds only this class.

You do not publish the jar yourself, and it is not committed. Launchpad's runner has no
Java, so `.github/workflows/automated-api-diagnosis-udf.yml` does it: on every push to
`main` or `launchpad` that touches this directory it builds, tests and publishes a GitHub
release named `emit-after-gap-<hash>`. The hash is of `pom.xml`, `build.sh` and `src/main/**`, computed
by the Terraform module [`tf/modules/udf-release`](../../tf/modules/udf-release), which the
workflow runs and Terraform uses too, so they always agree on the name. A release is never
replaced and nothing has to be committed to say which jar a commit wants: change the source
and push.

The release appears a minute or two after the push. A launch before then fails with a
message saying the jar is not published yet: launch again when the workflow has finished.
Do not delete a release: a commit whose source hashes to it needs it.

## Deployment

`tf/ai.tf` does it, as part of the demo. It computes the release tag from this source,
downloads `emit-after-gap.jar` from that release, uploads it with `confluent_flink_artifact`,
and the statement from `tf/flink/create_emit_after_gap.sql.tftpl` runs

```sql
CREATE FUNCTION EmitAfterGap AS 'org.example.EmitAfterGap' USING JAR 'confluent-artifact://<artifact id>'
```

Rename the class or package and that file must change too (`contract_test.go` checks). The
function is registered once per demo, so a new jar reaches a running demo only after
teardown and a new launch.
