# Renders the whole configuration against mock providers, so it needs no AWS or
# Confluent account, and checks what the task definition and the Flink statements end up
# saying.
#
#   terraform init -backend=false && terraform test
#
# It does not prove the demo works in the cloud. It catches a container that lost its
# secret, a port that moved, an image that would not be pulled, or a Flink statement that
# no longer waits for the table it reads.

mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b", "us-east-1c"] }
  }
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
  mock_resource "aws_secretsmanager_secret" {
    defaults = { arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:demo-abc123" }
  }
  mock_resource "aws_lb" {
    defaults = {
      arn      = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/demo/abc"
      dns_name = "demo-123.us-east-1.elb.amazonaws.com"
    }
  }
  mock_resource "aws_lb_target_group" {
    defaults = { arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/demo/abc" }
  }
  mock_data "aws_security_group" {
    defaults = { id = "sg-cloudfront" }
  }
  mock_resource "aws_cloudfront_vpc_origin" {
    defaults = { id = "vo-123" }
  }
  mock_resource "aws_cloudfront_distribution" {
    defaults = { domain_name = "d111111abcdef8.cloudfront.net" }
  }
  mock_resource "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::123456789012:role/demo" }
  }
}

mock_provider "confluent" {
  mock_data "confluent_organization" {
    defaults = { id = "org-1" }
  }
  mock_data "confluent_schema_registry_cluster" {
    defaults = {
      id            = "lsrc-1"
      api_version   = "srcm/v2"
      kind          = "Cluster"
      rest_endpoint = "https://psrc-1.us-east-1.aws.confluent.cloud"
    }
  }
  mock_data "confluent_flink_region" {
    defaults = {
      id            = "aws.us-east-1"
      api_version   = "fcpm/v2"
      kind          = "Region"
      rest_endpoint = "https://flink.us-east-1.aws.confluent.cloud"
    }
  }
  mock_resource "confluent_kafka_cluster" {
    defaults = {
      id                 = "lkc-1"
      api_version        = "cmk/v2"
      kind               = "Cluster"
      bootstrap_endpoint = "SASL_SSL://pkc-1.us-east-1.aws.confluent.cloud:9092"
      rest_endpoint      = "https://pkc-1.us-east-1.aws.confluent.cloud:443"
      rbac_crn           = "crn://confluent.cloud/organization=org-1/environment=env-1/cloud-cluster=lkc-1"
    }
  }
  mock_resource "confluent_service_account" {
    defaults = {
      id          = "sa-1"
      api_version = "iam/v2"
      kind        = "ServiceAccount"
    }
  }
  mock_resource "confluent_environment" {
    defaults = {
      id            = "env-1"
      resource_name = "crn://confluent.cloud/organization=org-1/environment=env-1"
    }
  }
  mock_resource "confluent_flink_compute_pool" {
    defaults = { id = "lfcp-1" }
  }
}

mock_provider "random" {
  mock_resource "random_password" {
    defaults = { result = "mockmockmockmockmockmockmockmock" }
  }
  mock_resource "random_id" {
    defaults = { hex = "ab12cd" }
  }
}

mock_provider "time" {}

# The jar is downloaded from a GitHub release. The mock stands for a published one: a zip
# (it starts with "PK") of a plausible size.
mock_provider "http" {
  mock_data "http" {
    defaults = {
      status_code          = 200
      response_body_base64 = "UEsDBHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eA=="
    }
  }
}

mock_provider "local" {}

variables {
  aws_region                 = "us-east-1"
  launchpad_tags             = { "launchpad:managed" = "true", "launchpad:demo" = "automated-api-diagnosis" }
  confluent_cloud_api_key    = "CCKEY"
  confluent_cloud_api_secret = "ccsecret"
}

run "task_definition" {
  command = apply

  assert {
    condition     = length(jsondecode(aws_ecs_task_definition.demo.container_definitions)) == 1 && jsondecode(aws_ecs_task_definition.demo.container_definitions)[0].name == "app"
    error_message = "the task must run exactly the app"
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.demo.container_definitions)[0].portMappings[0].containerPort == 8080
    error_message = "the load balancer reaches the app on 8080"
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.demo.container_definitions)[0].image == "ghcr.io/buddhike/automated-api-diagnosis:latest"
    error_message = "the image comes from the registry and tag variables"
  }

  assert {
    condition     = length([for e in jsondecode(aws_ecs_task_definition.demo.container_definitions)[0].environment : e if can(regex("(?i)secret|password|token|key$", e.name))]) == 0
    error_message = "a secret is in a plain environment variable; it must come from Secrets Manager"
  }

  assert {
    condition     = length(jsondecode(aws_ecs_task_definition.demo.container_definitions)[0].secrets) == 5 && alltrue([for s in jsondecode(aws_ecs_task_definition.demo.container_definitions)[0].secrets : startswith(s.valueFrom, "arn:aws:secretsmanager:")])
    error_message = "every container secret is a Secrets Manager reference"
  }

  assert {
    condition     = { for e in jsondecode(aws_ecs_task_definition.demo.container_definitions)[0].environment : e.name => e.value }["APIDIAG_AI_REPORTS"] == "true"
    error_message = "the agent is always deployed, so the app is told there is one"
  }

  assert {
    condition     = aws_ecs_service.demo.deployment_maximum_percent == 100 && aws_ecs_service.demo.deployment_minimum_healthy_percent == 0 && aws_ecs_service.demo.desired_count == 1
    error_message = "there must never be two tasks at once: two simulators would double the traffic"
  }

  assert {
    condition     = startswith(nonsensitive(output.launchpad_url), "https://d111111abcdef8.cloudfront.net/?access=") && length(nonsensitive(random_password.access_token.result)) >= 24
    error_message = "launchpad_url must be an HTTPS address on CloudFront, with an access token"
  }

  assert {
    condition     = aws_lb.demo.internal && aws_cloudfront_distribution.demo.default_cache_behavior[0].viewer_protocol_policy == "redirect-to-https"
    error_message = "the load balancer must be internal and viewers must be sent to HTTPS"
  }
}

run "flink_statements" {
  command = apply

  assert {
    condition     = confluent_flink_statement.detect_anomalies.properties["sql.tables.scan.startup.mode"] == "earliest-offset"
    error_message = "the detector must learn from everything the simulator has sent"
  }

  assert {
    condition     = confluent_flink_statement.alter_watermark.properties["sql.current-database"] == confluent_kafka_cluster.demo.display_name && confluent_flink_statement.alter_watermark.properties["sql.current-catalog"] == confluent_environment.demo.display_name
    error_message = "unqualified table names must resolve in the demo's environment and cluster"
  }

  assert {
    condition     = length(confluent_flink_statement.alter_watermark.statement) > 0 && length(confluent_flink_statement.detect_anomalies.statement) > 0
    error_message = "the statements are read from tf/flink/"
  }

  assert {
    condition     = confluent_flink_compute_pool.demo.max_cfu == 10
    error_message = "the pool's default size"
  }
}

run "ai_agent" {
  command = apply

  # The Bedrock key is made here and may do one thing.
  assert {
    condition     = one(data.aws_iam_policy_document.invoke_model.statement).actions == toset(["bedrock:InvokeModel"]) && one(data.aws_iam_policy_document.invoke_model.statement).resources == toset(["arn:aws:bedrock:ap-southeast-2::foundation-model/openai.gpt-oss-120b-1:0"])
    error_message = "the Flink user may invoke the one model and nothing else"
  }

  assert {
    condition     = confluent_flink_connection.bedrock.type == "BEDROCK" && confluent_flink_connection.bedrock.endpoint == "https://bedrock-runtime.ap-southeast-2.amazonaws.com/model/openai.gpt-oss-120b-1:0/invoke"
    error_message = "the connection must call the model in the Bedrock region, not the demo's"
  }

  assert {
    condition     = confluent_flink_connection.bedrock.aws_access_key == aws_iam_access_key.flink_llm.id
    error_message = "the connection uses the key Terraform made"
  }

  # No statement may carry a credential: the key reaches Confluent only through the connection.
  assert {
    condition = alltrue([for st in [
      confluent_flink_statement.ai_model, confluent_flink_statement.ai_agent, confluent_flink_statement.ai_reports,
      confluent_flink_statement.emit_after_gap, confluent_flink_statement.create_anomalies,
    ] : !strcontains(st.statement, aws_iam_access_key.flink_llm.id) && !strcontains(st.statement, aws_iam_access_key.flink_llm.secret)])
    error_message = "a Flink statement contains the Bedrock key"
  }

  assert {
    condition     = strcontains(confluent_flink_statement.ai_model.statement, "'bedrock.connection' = 'automated-api-diagnosis-ab12cd-bedrock'")
    error_message = "the model must use the connection Terraform made"
  }

  assert {
    condition     = strcontains(confluent_flink_statement.emit_after_gap.statement, "confluent-artifact://${confluent_flink_artifact.emit_after_gap.id}")
    error_message = "the function must be registered from the artifact Terraform uploaded"
  }
}

run "emit_after_gap_jar" {
  command = apply

  # The tag is a hash of the function's source, so nothing committed has to name a jar.
  assert {
    condition     = can(regex("^https://github\\.com/buddhike/bds-cflt-assets/releases/download/emit-after-gap-[0-9a-f]{16}/emit-after-gap\\.jar$", data.http.emit_after_gap_jar.url))
    error_message = "the jar comes from the release named by the source hash"
  }

  assert {
    condition     = confluent_flink_artifact.emit_after_gap.artifact_file == local_file.emit_after_gap_jar.filename
    error_message = "the artifact is the downloaded jar"
  }
}

run "emit_after_gap_jar_not_published_yet" {
  command = plan

  override_data {
    target = data.http.emit_after_gap_jar
    values = {
      status_code          = 404
      response_body_base64 = "Tm90IEZvdW5k"
    }
  }

  # A launch ahead of the release workflow must fail with a message that says what to do,
  # not upload a "Not Found" page as a jar.
  expect_failures = [local_file.emit_after_gap_jar]
}

run "emit_after_gap_download_is_not_a_jar" {
  command = plan

  override_data {
    target = data.http.emit_after_gap_jar
    values = {
      status_code          = 200
      response_body_base64 = "PGh0bWw+Y2FwdGl2ZSBwb3J0YWw8L2h0bWw+"
    }
  }

  expect_failures = [local_file.emit_after_gap_jar]
}

run "bedrock_region_and_model" {
  command = apply

  variables {
    bedrock_region   = "us-west-2"
    bedrock_model_id = "openai.gpt-oss-20b-1:0"
  }

  assert {
    condition     = one(data.aws_iam_policy_document.invoke_model.statement).resources == toset(["arn:aws:bedrock:us-west-2::foundation-model/openai.gpt-oss-20b-1:0"]) && startswith(confluent_flink_connection.bedrock.endpoint, "https://bedrock-runtime.us-west-2.amazonaws.com/model/openai.gpt-oss-20b-1:0/")
    error_message = "the policy and the connection must follow the model and region variables"
  }
}

run "private_registry" {
  command = apply

  variables {
    registry_credentials_secret_arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-xyz"
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.demo.container_definitions)[0].repositoryCredentials.credentialsParameter == "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-xyz"
    error_message = "a private image needs the registry credentials"
  }
}
