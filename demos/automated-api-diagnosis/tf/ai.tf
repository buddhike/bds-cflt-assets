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
# sustained failure gets one report, not one per 10-second window.
#
# Its jar is not in this repository. The runner has no Java toolchain, so a GitHub workflow
# builds the Maven project in ../udf/emit-after-gap and publishes the jar as a release
# whose tag is a hash of that source (modules/udf-release). Terraform computes the same tag
# from the source it was launched with and downloads the jar from that release. Nothing is
# committed to say which jar a commit wants, and an old commit keeps getting its own jar
# because a release is never overwritten.
#
# The release appears a minute or two after a change to the source reaches the repository.
# Until then a launch fails here with the 404 below: launch again once the workflow is done.
#
# Terraform registers the function once, so a changed jar reaches a running demo only after
# teardown and a new launch (see ignore_changes).
module "udf_release" {
  source     = "./modules/udf-release"
  source_dir = "${path.module}/../udf/emit-after-gap"
}

data "http" "emit_after_gap_jar" {
  url = "https://github.com/${var.udf_release_repository}/releases/download/${module.udf_release.tag}/emit-after-gap.jar"

  # Terraform reads this on destroy too, so a stalled connection must give up, not hold the
  # teardown until the runner's time limit. A missing release is not an error here (the
  # precondition below catches it for a launch), so teardown does not need one.
  request_timeout_ms = 30000
  retry {
    attempts     = 3
    min_delay_ms = 2000
  }
}

# The provider wants a file, not a URL. The runner's disk is empty on every invocation,
# and local_file writes the jar again when it finds it missing.
resource "local_file" "emit_after_gap_jar" {
  filename       = "${path.module}/.udf/emit-after-gap.jar"
  content_base64 = data.http.emit_after_gap_jar.response_body_base64

  lifecycle {
    # Conditions on the resource, not the data source, so destroying the demo does not
    # need the release to exist.
    precondition {
      condition     = data.http.emit_after_gap_jar.status_code == 200
      error_message = "The EmitAfterGap jar is not published at ${data.http.emit_after_gap_jar.url} (HTTP ${data.http.emit_after_gap_jar.status_code}). The automated-api-diagnosis-udf workflow creates the release after a change to udf/emit-after-gap reaches the repository: wait for it to finish, then try again."
    }

    # A zip starts with "PK". Anything else with a 200 (an error page from a proxy, a
    # truncated body) would be uploaded as a jar.
    precondition {
      condition     = startswith(data.http.emit_after_gap_jar.response_body_base64, "UEsD") && length(data.http.emit_after_gap_jar.response_body_base64) > 1000
      error_message = "What was downloaded from ${data.http.emit_after_gap_jar.url} is not a jar."
    }
  }
}

resource "confluent_flink_artifact" "emit_after_gap" {
  display_name   = "${local.name}-emit-after-gap"
  cloud          = "AWS"
  region         = var.aws_region
  content_format = "JAR"
  artifact_file  = local_file.emit_after_gap_jar.filename

  environment {
    id = confluent_environment.demo.id
  }

  lifecycle {
    # The function is registered once (a CREATE FUNCTION cannot be repeated), so replacing
    # the artifact on a running demo would only make the apply fail. A new jar takes
    # effect when the demo is launched again.
    ignore_changes = [artifact_file]
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
