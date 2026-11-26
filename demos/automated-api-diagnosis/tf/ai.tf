# The AI agent that writes up each anomaly, running on Amazon Bedrock.
#
#   api_anomalies ──► EmitAfterGap (one row per API and status per 5 minutes)
#                 ──► AI_RUN_AGENT ──► Bedrock ──► api_anomaly_ai_reports
#
# Credentials. Launchpad passes no variables except its own, so Terraform makes the key
# Flink calls Bedrock with: an IAM user that may invoke this one model and nothing else.
# Nothing is written down by hand. The key goes from aws_iam_access_key straight into
# confluent_flink_connection, both marked sensitive, so Terraform does not print it
# in plan or apply output (keep TF_LOG unset: its debug levels can log provider requests),
# and it is in no file of this repository. It is in Terraform's state, in
# Launchpad's bucket, like the other secrets this demo makes, and it is deleted with
# the demo (the user and its key are destroyed, and Confluent drops the connection with
# the environment). Never give the key an output.

locals {
  # The model's ARN: a foundation model has no account id. The policy below allows this
  # model in this region and nothing else.
  bedrock_model_arn = "arn:${data.aws_partition.current.partition}:bedrock:${var.bedrock_region}::foundation-model/${var.bedrock_model_id}"
}

data "aws_iam_policy_document" "invoke_model" {
  statement {
    actions   = ["bedrock:InvokeModel"]
    resources = [local.bedrock_model_arn]
  }
}

resource "aws_iam_user" "flink_llm" {
  name          = "${local.name}-flink-llm"
  force_destroy = true
}

resource "aws_iam_user_policy" "flink_llm" {
  name   = "invoke-model"
  user   = aws_iam_user.flink_llm.name
  policy = data.aws_iam_policy_document.invoke_model.json
}

resource "aws_iam_access_key" "flink_llm" {
  user = aws_iam_user.flink_llm.name
}

# An IAM key takes a few seconds to be usable everywhere. Confluent checks nothing when it
# stores the connection, but give the key time before the first statement calls Bedrock.
resource "time_sleep" "bedrock_key" {
  create_duration = "15s"

  depends_on = [aws_iam_access_key.flink_llm, aws_iam_user_policy.flink_llm]
}

resource "confluent_flink_connection" "bedrock" {
  display_name = "${local.name}-bedrock"
  type         = "BEDROCK"

  endpoint      = "https://bedrock-runtime.${var.bedrock_region}.amazonaws.com/model/${var.bedrock_model_id}/invoke"
  rest_endpoint = data.confluent_flink_region.demo.rest_endpoint

  aws_access_key = aws_iam_access_key.flink_llm.id
  aws_secret_key = aws_iam_access_key.flink_llm.secret

  environment {
    id = confluent_environment.demo.id
  }
  organization {
    id = data.confluent_organization.this.id
  }
  credentials {
    key    = confluent_api_key.flink.id
    secret = confluent_api_key.flink.secret
  }
  compute_pool {
    id = confluent_flink_compute_pool.demo.id
  }
  principal {
    id = confluent_service_account.demo.id
  }

  depends_on = [confluent_role_binding.flink_admin, time_sleep.bedrock_key]
}

# EmitAfterGap is a process table function: it passes on the first row of each
# (api_id, http_status) and then drops the rest until five minutes have passed, so a
# sustained failure gets one report, not one per 10-second window. The source is the Maven
# project in ../udf/emit-after-gap, and the jar here is built from it by its build.sh. The
# jar is committed because the runner has no Java toolchain; CI checks it matches the source.
# Terraform does not notice a changed jar on a running demo (the function is registered
# once), so a new jar takes effect when the demo is torn down and launched again.
resource "confluent_flink_artifact" "emit_after_gap" {
  display_name   = "${local.name}-emit-after-gap"
  cloud          = "AWS"
  region         = var.aws_region
  content_format = "JAR"
  artifact_file  = "${path.module}/udf/emit-after-gap.jar"

  environment {
    id = confluent_environment.demo.id
  }
}

resource "confluent_flink_statement" "emit_after_gap" {
  statement_name = "${local.name}-emit-after-gap"
  statement      = templatefile("${path.module}/flink/create_emit_after_gap.sql.tftpl", { artifact_id = confluent_flink_artifact.emit_after_gap.id })
  properties     = local.flink_properties

  compute_pool { id = confluent_flink_compute_pool.demo.id }
  principal { id = confluent_service_account.demo.id }
  environment { id = confluent_environment.demo.id }
  organization { id = data.confluent_organization.this.id }
  credentials {
    key    = confluent_api_key.flink.id
    secret = confluent_api_key.flink.secret
  }
  rest_endpoint = data.confluent_flink_region.demo.rest_endpoint
}

resource "confluent_flink_statement" "ai_model" {
  statement_name = "${local.name}-ai-model"
  statement      = templatefile("${path.module}/flink/ai_model.sql.tftpl", { connection = confluent_flink_connection.bedrock.display_name })
  properties     = local.flink_properties

  compute_pool { id = confluent_flink_compute_pool.demo.id }
  principal { id = confluent_service_account.demo.id }
  environment { id = confluent_environment.demo.id }
  organization { id = data.confluent_organization.this.id }
  credentials {
    key    = confluent_api_key.flink.id
    secret = confluent_api_key.flink.secret
  }
  rest_endpoint = data.confluent_flink_region.demo.rest_endpoint
}

resource "confluent_flink_statement" "ai_agent" {
  statement_name = "${local.name}-ai-agent"
  statement      = file("${path.module}/flink/ai_agent.sql")
  properties     = local.flink_properties

  compute_pool { id = confluent_flink_compute_pool.demo.id }
  principal { id = confluent_service_account.demo.id }
  environment { id = confluent_environment.demo.id }
  organization { id = data.confluent_organization.this.id }
  credentials {
    key    = confluent_api_key.flink.id
    secret = confluent_api_key.flink.secret
  }
  rest_endpoint = data.confluent_flink_region.demo.rest_endpoint

  depends_on = [confluent_flink_statement.ai_model]
}

# Runs for as long as the demo does, reading api_anomalies from its tail.
resource "confluent_flink_statement" "ai_reports" {
  statement_name = "${local.name}-ai-reports"
  statement      = file("${path.module}/flink/ai_reports.sql")
  properties     = local.flink_properties

  compute_pool { id = confluent_flink_compute_pool.demo.id }
  principal { id = confluent_service_account.demo.id }
  environment { id = confluent_environment.demo.id }
  organization { id = data.confluent_organization.this.id }
  credentials {
    key    = confluent_api_key.flink.id
    secret = confluent_api_key.flink.secret
  }
  rest_endpoint = data.confluent_flink_region.demo.rest_endpoint

  depends_on = [
    confluent_flink_statement.ai_agent,
    confluent_flink_statement.emit_after_gap,
    confluent_flink_statement.create_anomalies,
    confluent_flink_statement.create_anomaly_reports,
    confluent_flink_statement.detect_anomalies,
  ]
}
