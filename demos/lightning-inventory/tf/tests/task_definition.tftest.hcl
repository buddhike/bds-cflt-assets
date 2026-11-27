# Renders the whole configuration against mock providers, so it needs no AWS or Confluent
# account, and checks what the three task definitions, the IAM policies, the service accounts'
# roles and the Lightning Table end up saying.
#
#   terraform init -backend=false && terraform test
#
# It does not prove the demo works in the cloud. It catches a container that lost its secret,
# a role that was given a credential it must not have, a permission that widened, a service
# account with a broader role than it needs, or a Lightning Table that Terraform stopped managing.

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
      dns_name = "internal-demo-123.us-east-1.elb.amazonaws.com"
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
  mock_resource "aws_ecs_cluster" {
    defaults = { name = "demo-cluster" }
  }
  mock_resource "aws_ecs_service" {
    defaults = { name = "demo-service" }
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
      resource_name = "crn://confluent.cloud/organization=org-1/environment=env-1/schema-registry=lsrc-1"
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
  mock_resource "confluent_schema" {
    defaults = { schema_identifier = 100001 }
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

# Each service account gets its own id, so a test can tell them apart. (An override at the top of
# the file applies from the first run: a run's own override would not touch an account an earlier
# run created.)
override_resource {
  target = confluent_service_account.provisioner
  values = { id = "sa-provisioner", api_version = "iam/v2", kind = "ServiceAccount" }
}

override_resource {
  target = confluent_service_account.producer
  values = { id = "sa-producer", api_version = "iam/v2", kind = "ServiceAccount" }
}

override_resource {
  target = confluent_service_account.query
  values = { id = "sa-query", api_version = "iam/v2", kind = "ServiceAccount" }
}

override_resource {
  target = confluent_service_account.control
  values = { id = "sa-control", api_version = "iam/v2", kind = "ServiceAccount" }
}

variables {
  aws_region                 = "us-east-1"
  launchpad_tags             = { "launchpad:managed" = "true", "launchpad:demo" = "lightning-inventory" }
  confluent_cloud_api_key    = "CCKEY"
  confluent_cloud_api_secret = "ccsecret"
}

run "task_definitions" {
  command = apply

  assert {
    condition     = length(aws_ecs_task_definition.role) == 3
    error_message = "three roles: control, producer and query"
  }

  assert {
    condition     = alltrue([for r, t in aws_ecs_task_definition.role : length(jsondecode(t.container_definitions)) == 1 && jsondecode(t.container_definitions)[0].name == "app" && jsondecode(t.container_definitions)[0].image == "ghcr.io/buddhike/lightning-inventory:latest"])
    error_message = "every task runs exactly the app, from the registry and tag variables"
  }

  assert {
    condition     = { for r, t in aws_ecs_task_definition.role : r => { for e in jsondecode(t.container_definitions)[0].environment : e.name => e.value }["LIGHTNING_ROLE"] } == { control = "control", producer = "producer", query = "query" }
    error_message = "each task definition sets its own role"
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.role["control"].container_definitions)[0].portMappings[0].containerPort == 8080 && length(jsondecode(aws_ecs_task_definition.role["producer"].container_definitions)[0].portMappings) == 0 && length(jsondecode(aws_ecs_task_definition.role["query"].container_definitions)[0].portMappings) == 0
    error_message = "only the control task takes connections, on 8080"
  }

  assert {
    condition     = alltrue([for r, t in aws_ecs_task_definition.role : length([for e in jsondecode(t.container_definitions)[0].environment : e if can(regex("(?i)secret|password|token|key$", e.name))]) == 0])
    error_message = "a secret is in a plain environment variable; it must come from Secrets Manager"
  }

  assert {
    condition     = alltrue([for r, t in aws_ecs_task_definition.role : alltrue([for s in jsondecode(t.container_definitions)[0].secrets : startswith(s.valueFrom, "arn:aws:secretsmanager:")])])
    error_message = "every container secret is a Secrets Manager reference"
  }

  # Least privilege: a role gets only the credentials it uses.
  assert {
    condition     = toset([for s in jsondecode(aws_ecs_task_definition.role["producer"].container_definitions)[0].secrets : s.name]) == toset(["LIGHTNING_WORKER_TOKEN", "LIGHTNING_KAFKA_API_KEY", "LIGHTNING_KAFKA_API_SECRET"])
    error_message = "a producer holds the worker token and the Kafka key, nothing else"
  }

  assert {
    condition     = toset([for s in jsondecode(aws_ecs_task_definition.role["query"].container_definitions)[0].secrets : s.name]) == toset(["LIGHTNING_WORKER_TOKEN", "LIGHTNING_GLOBAL_API_KEY", "LIGHTNING_GLOBAL_API_SECRET"])
    error_message = "a query task holds the worker token and the Global key, nothing else"
  }

  assert {
    condition     = !contains([for s in jsondecode(aws_ecs_task_definition.role["producer"].container_definitions)[0].secrets : s.name], "LIGHTNING_ACCESS_TOKEN") && !contains([for s in jsondecode(aws_ecs_task_definition.role["query"].container_definitions)[0].secrets : s.name], "LIGHTNING_ACCESS_TOKEN")
    error_message = "only the control task may hold the access token, which opens the UI"
  }

  assert {
    condition     = toset([for s in jsondecode(aws_ecs_task_definition.role["control"].container_definitions)[0].secrets : s.name]) == toset(["LIGHTNING_ACCESS_TOKEN", "LIGHTNING_WORKER_TOKEN", "LIGHTNING_CLOUD_API_KEY", "LIGHTNING_CLOUD_API_SECRET"])
    error_message = "the control task holds the two tokens and the read-only Cloud key, no Global or Kafka key"
  }

  assert {
    condition     = { for e in jsondecode(aws_ecs_task_definition.role["producer"].container_definitions)[0].environment : e.name => e.value }["LIGHTNING_VALUE_SCHEMA_ID"] == "100001" && { for e in jsondecode(aws_ecs_task_definition.role["producer"].container_definitions)[0].environment : e.name => e.value }["LIGHTNING_KAFKA_BOOTSTRAP"] == "pkc-1.us-east-1.aws.confluent.cloud:9092"
    error_message = "the producer encodes with the schema Terraform registered and connects to the cluster's bootstrap"
  }

  assert {
    condition     = { for e in jsondecode(aws_ecs_task_definition.role["query"].container_definitions)[0].environment : e.name => e.value }["LIGHTNING_CONTROL_URL"] == "http://internal-demo-123.us-east-1.elb.amazonaws.com"
    error_message = "the workers poll the control service through the internal load balancer"
  }

  assert {
    condition     = { for e in jsondecode(aws_ecs_task_definition.role["producer"].container_definitions)[0].environment : e.name => e.value }["LIGHTNING_LOCATIONS_PER_SKU"] == { for e in jsondecode(aws_ecs_task_definition.role["query"].container_definitions)[0].environment : e.name => e.value }["LIGHTNING_LOCATIONS_PER_SKU"]
    error_message = "producers and query tasks must agree on the catalogue"
  }
}

run "services" {
  command = apply

  assert {
    condition     = aws_ecs_service.control.desired_count == 1 && aws_ecs_service.control.deployment_maximum_percent == 100 && aws_ecs_service.control.deployment_minimum_healthy_percent == 0
    error_message = "never two control tasks: one holds the rates"
  }

  assert {
    condition     = aws_ecs_service.control.wait_for_steady_state && alltrue([for k, s in aws_ecs_service.worker : s.wait_for_steady_state])
    error_message = "Terraform waits until the tasks are healthy"
  }

  assert {
    condition     = aws_appautoscaling_target.worker["producer"].min_capacity == 1 && aws_appautoscaling_target.worker["producer"].max_capacity == 4 && aws_appautoscaling_target.worker["query"].min_capacity == 1 && aws_appautoscaling_target.worker["query"].max_capacity == 8
    error_message = "both worker services can scale out"
  }

  assert {
    condition     = alltrue([for k, p in aws_appautoscaling_policy.worker_cpu : p.policy_type == "TargetTrackingScaling"])
    error_message = "the workers scale on CPU"
  }

  assert {
    condition     = aws_lb.demo.internal && aws_cloudfront_distribution.demo.default_cache_behavior[0].viewer_protocol_policy == "redirect-to-https"
    error_message = "the load balancer must be internal and viewers must be sent to HTTPS"
  }
}

run "aws_permissions" {
  command = apply

  # The deliberate exception to "the app has no AWS permissions".
  assert {
    condition     = jsondecode(aws_iam_role_policy.query.policy).Statement[0].Action == ["cloudwatch:PutMetricData"] && length(jsondecode(aws_iam_role_policy.query.policy).Statement) == 1
    error_message = "the query task may publish metrics and do nothing else"
  }

  assert {
    condition     = jsondecode(aws_iam_role_policy.query.policy).Statement[0].Condition.StringEquals["cloudwatch:namespace"] == "LightningInventory/lightning-inventory-ab12cd" && output.metrics_namespace == "LightningInventory/lightning-inventory-ab12cd"
    error_message = "PutMetricData must be restricted to this deployment's namespace"
  }

  assert {
    condition     = jsondecode(aws_iam_role_policy.control.policy).Statement[0].Action == ["cloudwatch:GetMetricData"] && length(jsondecode(aws_iam_role_policy.control.policy).Statement) == 1
    error_message = "the control task may read metrics and do nothing else"
  }

  assert {
    condition     = aws_ecs_task_definition.role["producer"].task_role_arn == null || aws_ecs_task_definition.role["producer"].task_role_arn == ""
    error_message = "the producer has no AWS permissions at all"
  }

  assert {
    condition     = aws_ecs_task_definition.role["control"].task_role_arn != null && aws_ecs_task_definition.role["query"].task_role_arn != null
    error_message = "control and query have their task roles"
  }
}

run "launchpad_interface" {
  command = apply

  assert {
    condition     = startswith(nonsensitive(output.launchpad_url), "https://d111111abcdef8.cloudfront.net/?access=") && length(nonsensitive(random_password.access_token.result)) >= 24
    error_message = "launchpad_url must be an HTTPS address on CloudFront, with an access token"
  }
}

run "topic_and_schema" {
  command = apply

  assert {
    condition     = confluent_kafka_topic.inventory.partitions_count == 6 && confluent_kafka_topic.inventory.config["cleanup.policy"] == "compact"
    error_message = "6 partitions, compacted, so the Lightning Table runs in upsert mode"
  }

  assert {
    condition     = confluent_kafka_topic.inventory.topic_name == "inventory_positions" && confluent_schema.inventory_value.subject_name == "inventory_positions-value"
    error_message = "a Lightning Table topic name has no hyphen, and the value schema is registered under it"
  }

  assert {
    condition     = confluent_kafka_cluster.demo.cloud == "AWS" && length(confluent_kafka_cluster.demo.standard) == 1
    error_message = "a Standard cluster on AWS, the only cloud with Lightning Tables"
  }

  assert {
    condition     = confluent_api_key.query_global.managed_resource[0].id == "global" && confluent_api_key.query_global.managed_resource[0].kind == "Global"
    error_message = "queries need a Global API key"
  }

  assert {
    condition     = length(confluent_api_key.control_cloud.managed_resource) == 0 && length(confluent_api_key.producer_kafka.managed_resource) == 1
    error_message = "the control key is a Cloud key (no managed resource); the producers' is a cluster key"
  }
}

run "lightning_table_is_terraforms" {
  command = apply

  assert {
    condition     = confluent_rtce_topic.inventory.topic_name == confluent_kafka_topic.inventory.topic_name && confluent_rtce_topic.inventory.cloud == "AWS" && confluent_rtce_topic.inventory.region == "us-east-1"
    error_message = "the Lightning Table is on the topic, on AWS, in the cluster's region"
  }

  assert {
    condition     = confluent_rtce_topic.inventory.environment[0].id == "env-1" && confluent_rtce_topic.inventory.kafka_cluster[0].id == "lkc-1"
    error_message = "the table is in the demo's environment and cluster"
  }
}

run "service_accounts_are_split" {
  command = apply

  # Only the provisioner, whose keys no task holds, has a broad role.
  assert {
    condition     = confluent_role_binding.provisioner.principal == "User:sa-provisioner" && confluent_role_binding.provisioner.role_name == "EnvironmentAdmin"
    error_message = "the provisioner creates the topic and the schema"
  }

  assert {
    condition     = alltrue([for b in [confluent_role_binding.producer_write, confluent_role_binding.query_read, confluent_role_binding.query_schema_read, confluent_role_binding.control_cluster_read, confluent_role_binding.control_metrics] : !contains(["EnvironmentAdmin", "CloudClusterAdmin", "OrganizationAdmin", "ResourceOwner"], b.role_name)])
    error_message = "no task's service account may hold an administrator role"
  }

  assert {
    condition     = confluent_role_binding.producer_write.principal == "User:sa-producer" && confluent_role_binding.producer_write.role_name == "DeveloperWrite" && endswith(confluent_role_binding.producer_write.crn_pattern, "/topic=inventory_positions")
    error_message = "the producers may write to the topic"
  }

  assert {
    condition     = confluent_role_binding.query_read.principal == "User:sa-query" && confluent_role_binding.query_read.role_name == "DeveloperRead" && endswith(confluent_role_binding.query_read.crn_pattern, "/topic=inventory_positions") && confluent_role_binding.query_schema_read.principal == "User:sa-query" && confluent_role_binding.query_schema_read.role_name == "DeveloperRead"
    error_message = "the query tasks may read the topic and Schema Registry"
  }

  assert {
    condition     = confluent_role_binding.control_cluster_read.principal == "User:sa-control" && confluent_role_binding.control_cluster_read.role_name == "DeveloperRead" && endswith(confluent_role_binding.control_cluster_read.crn_pattern, "/topic=*") && confluent_role_binding.control_metrics.principal == "User:sa-control" && confluent_role_binding.control_metrics.role_name == "MetricsViewer" && confluent_role_binding.control_metrics.crn_pattern == confluent_kafka_cluster.demo.rbac_crn
    error_message = "the control task reads the topic's table status and the cluster's metrics, and changes nothing"
  }

  assert {
    condition     = confluent_api_key.producer_kafka.owner[0].id == "sa-producer" && confluent_api_key.query_global.owner[0].id == "sa-query" && confluent_api_key.control_cloud.owner[0].id == "sa-control" && confluent_api_key.provisioner_kafka.owner[0].id == "sa-provisioner" && confluent_api_key.provisioner_schema_registry.owner[0].id == "sa-provisioner"
    error_message = "each key belongs to its own service account"
  }
}

run "region_without_lightning_tables_is_refused" {
  command = plan

  variables {
    aws_region = "ca-central-1"
  }

  expect_failures = [var.aws_region]
}

run "image_follows_the_commit" {
  command = apply

  variables {
    launchpad_ref = "0123456789abcdef0123456789abcdef01234567"
  }

  assert {
    condition     = alltrue([for r, t in aws_ecs_task_definition.role : jsondecode(t.container_definitions)[0].image == "ghcr.io/buddhike/lightning-inventory:0123456789abcdef0123456789abcdef01234567"])
    error_message = "every task runs the image of the commit Launchpad downloaded"
  }
}

run "image_tag_overrides_the_commit" {
  command = apply

  variables {
    launchpad_ref = "0123456789abcdef0123456789abcdef01234567"
    image_tag     = "main"
  }

  assert {
    condition     = alltrue([for r, t in aws_ecs_task_definition.role : jsondecode(t.container_definitions)[0].image == "ghcr.io/buddhike/lightning-inventory:main"])
    error_message = "an explicit image_tag wins"
  }
}

run "private_registry" {
  command = apply

  variables {
    registry_credentials_secret_arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-xyz"
  }

  assert {
    condition     = alltrue([for r, t in aws_ecs_task_definition.role : jsondecode(t.container_definitions)[0].repositoryCredentials.credentialsParameter == "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-xyz"])
    error_message = "a private image needs the registry credentials"
  }
}
