# Renders the whole configuration against mock providers, so it needs no AWS or
# Confluent account, and checks what the task definition ends up saying.
#
#   terraform init -backend=false && terraform test
#
# It does not prove the demo works in the cloud. It catches a container that lost
# its secret, a port that moved, or an image that would not be pulled.

mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b", "us-east-1c"] }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012" }
  }
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
  mock_resource "aws_secretsmanager_secret" {
    defaults = { arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:pims-abc123" }
  }
  mock_resource "aws_lb" {
    defaults = {
      arn      = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/pims/abc"
      dns_name = "pims-123.us-east-1.elb.amazonaws.com"
    }
  }
  mock_resource "aws_lb_target_group" {
    defaults = { arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/pims/abc" }
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
    defaults = { arn = "arn:aws:iam::123456789012:role/pims" }
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
  mock_resource "confluent_kafka_cluster" {
    defaults = {
      id                 = "lkc-1"
      api_version        = "cmk/v2"
      kind               = "Cluster"
      bootstrap_endpoint = "SASL_SSL://pkc-1.us-east-1.aws.confluent.cloud:9092"
      rest_endpoint      = "https://pkc-1.us-east-1.aws.confluent.cloud:443"
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
}

mock_provider "random" {
  mock_resource "random_password" {
    defaults = { result = "mockmockmockmockmockmockmockmock" }
  }
}

variables {
  aws_region                 = "us-east-1"
  launchpad_tags             = { "launchpad:managed" = "true", "launchpad:demo" = "pims" }
  confluent_cloud_api_key    = "CCKEY"
  confluent_cloud_api_secret = "ccsecret"
}

run "task_definition" {
  command = apply

  assert {
    condition     = join(",", sort(keys({ for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }))) == "connect,duckdb,mysql,pims-server"
    error_message = "the task must run exactly mysql, connect, duckdb and pims-server"
  }

  assert {
    condition     = { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["pims-server"].portMappings[0].containerPort == 8080 && length(try({ for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["connect"].portMappings, [])) == 0
    error_message = "only the app is published; Connect, MySQL and the agent stay inside the task"
  }

  assert {
    condition     = { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["pims-server"].image == "ghcr.io/buddhike/pims-server:latest" && { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["duckdb"].image == "ghcr.io/buddhike/pims-duckdb:latest" && { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["connect"].image == "ghcr.io/buddhike/pims-connect:latest"
    error_message = "images come from the registry and tag variables"
  }

  assert {
    condition     = alltrue([for c in values({ for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }) : length([for e in try(c.environment, []) : e if can(regex("(?i)secret|password|token|jaas", e.name)) && !can(regex("_TOPIC$", e.name))]) == 0])
    error_message = "a secret is in a plain environment variable; it must come from Secrets Manager"
  }

  assert {
    condition     = alltrue([for c in values({ for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }) : alltrue([for s in try(c.secrets, []) : startswith(s.valueFrom, "arn:aws:secretsmanager:")])])
    error_message = "every container secret is a Secrets Manager reference"
  }

  assert {
    condition     = { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["mysql"].healthCheck.command[1] == "mysqladmin" && { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["connect"].dependsOn[0].condition == "HEALTHY"
    error_message = "Connect must wait for a healthy MySQL"
  }

  assert {
    condition     = { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["duckdb"].essential == false && { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["mysql"].essential == true && { for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) : c.name => c }["pims-server"].essential == true
    error_message = "losing DuckDB must not restart the task and drop MySQL's data"
  }

  assert {
    condition     = aws_ecs_service.pims.deployment_maximum_percent == 100 && aws_ecs_service.pims.deployment_minimum_healthy_percent == 0 && aws_ecs_service.pims.desired_count == 1
    error_message = "there must never be two tasks at once"
  }

  assert {
    condition     = startswith(nonsensitive(output.launchpad_url), "https://d111111abcdef8.cloudfront.net/?access=")
    error_message = "launchpad_url must be an HTTPS address on CloudFront"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.launchpad_shutdown)).url == "https://d111111abcdef8.cloudfront.net/shutdown" && length(jsondecode(nonsensitive(output.launchpad_shutdown)).token) >= 24
    error_message = "launchpad_shutdown must give Launchpad an HTTPS address and the access token"
  }

  assert {
    condition     = aws_lb.pims.internal && aws_cloudfront_distribution.pims.default_cache_behavior[0].viewer_protocol_policy == "redirect-to-https"
    error_message = "the load balancer must be internal and viewers must be sent to HTTPS"
  }
}

run "images_follow_the_commit" {
  command = apply

  variables {
    launchpad_ref = "0123456789abcdef0123456789abcdef01234567"
  }

  assert {
    condition = alltrue([
      for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) :
      c.name == "mysql" || endswith(c.image, ":0123456789abcdef0123456789abcdef01234567")
    ])
    error_message = "the three images are the ones of the commit Launchpad downloaded"
  }
}

run "private_registry" {
  command = apply

  variables {
    registry_credentials_secret_arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-xyz"
  }

  assert {
    condition = alltrue([
      for c in jsondecode(aws_ecs_task_definition.pims.container_definitions) :
      c.name == "mysql" || c.repositoryCredentials.credentialsParameter == "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-xyz"
    ])
    error_message = "private images need the registry credentials, and the public MySQL image must not get them"
  }
}
