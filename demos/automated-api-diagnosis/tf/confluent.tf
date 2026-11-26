# Confluent Cloud: the environment, cluster, topic and schema, with the keys the app
# and Flink use.

locals {
  # The simulator's topic. The Flink statements in tf/flink/ and the app's default
  # (APIDIAG_TOPIC) use the same name, which contract_test.go checks.
  topic = "gateway-logs"

  # Environment and service account names are unique within a Confluent Cloud
  # organisation, so a random suffix lets several people run the demo in one.
  name = "${var.launchpad_demo_id}-${random_id.suffix.hex}"
}

resource "random_id" "suffix" {
  byte_length = 3
}

data "confluent_organization" "this" {}

resource "confluent_environment" "demo" {
  display_name = local.name

  # Provisions Schema Registry, which the Avro schema below lives in.
  stream_governance {
    package = "ESSENTIALS"
  }
}

resource "confluent_kafka_cluster" "demo" {
  display_name = local.name
  availability = "SINGLE_ZONE"
  cloud        = "AWS"
  region       = var.aws_region
  standard {}

  environment {
    id = confluent_environment.demo.id
  }
}

# Schema Registry is provisioned automatically with the first cluster.
data "confluent_schema_registry_cluster" "demo" {
  environment {
    id = confluent_environment.demo.id
  }
  depends_on = [confluent_kafka_cluster.demo]
}

# One service account owns every key the app and Flink use. It is scoped to the demo's
# environment, so it cannot touch anything else in the organisation.
resource "confluent_service_account" "demo" {
  display_name = "${local.name}-${confluent_environment.demo.id}"
  description  = "Automated API diagnosis demo: the simulator, the Flink statements and the monitor"
}

resource "confluent_role_binding" "env_admin" {
  principal   = "User:${confluent_service_account.demo.id}"
  role_name   = "EnvironmentAdmin"
  crn_pattern = confluent_environment.demo.resource_name
}

# Flink runs its statements as this account, which needs Flink's own role as well.
resource "confluent_role_binding" "flink_admin" {
  principal   = "User:${confluent_service_account.demo.id}"
  role_name   = "FlinkAdmin"
  crn_pattern = confluent_environment.demo.resource_name
}

# Flink resolves the Kafka cluster as a database, which needs cluster access of its own:
# without it every statement fails with "A database with name or id ... cannot be resolved".
resource "confluent_role_binding" "cluster_admin" {
  principal   = "User:${confluent_service_account.demo.id}"
  role_name   = "CloudClusterAdmin"
  crn_pattern = confluent_kafka_cluster.demo.rbac_crn
}

resource "confluent_api_key" "kafka" {
  display_name = "${local.name}-kafka"
  description  = "The simulator and the monitor, and Terraform creating the topic"

  owner {
    id          = confluent_service_account.demo.id
    api_version = confluent_service_account.demo.api_version
    kind        = confluent_service_account.demo.kind
  }

  managed_resource {
    id          = confluent_kafka_cluster.demo.id
    api_version = confluent_kafka_cluster.demo.api_version
    kind        = confluent_kafka_cluster.demo.kind

    environment {
      id = confluent_environment.demo.id
    }
  }

  depends_on = [confluent_role_binding.env_admin, confluent_role_binding.cluster_admin]
}

resource "confluent_api_key" "schema_registry" {
  display_name = "${local.name}-schema-registry"
  description  = "The simulator's and the monitor's Avro serializers, and Terraform registering the schema"

  owner {
    id          = confluent_service_account.demo.id
    api_version = confluent_service_account.demo.api_version
    kind        = confluent_service_account.demo.kind
  }

  managed_resource {
    id          = data.confluent_schema_registry_cluster.demo.id
    api_version = data.confluent_schema_registry_cluster.demo.api_version
    kind        = data.confluent_schema_registry_cluster.demo.kind

    environment {
      id = confluent_environment.demo.id
    }
  }

  depends_on = [confluent_role_binding.env_admin]
}

# A key is not always usable the moment its create call returns. Using it at once, to
# register the schema, can fail with 401 Unauthorized. Terraform orders the calls but not
# how long the backend takes to catch up, which is what Confluent's own examples wait for.
resource "time_sleep" "wait_for_keys" {
  depends_on      = [confluent_api_key.schema_registry, confluent_api_key.kafka]
  create_duration = "30s"
}

# The schema comes before the topic, so Flink never sees the topic without one: a topic
# Flink has read with no schema stays a single opaque BYTES column for good.
#
# gateway-logs-value.avsc is the schema the Go producer registers itself. A test in
# internal/server (contract_test.go) keeps the two the same.
resource "confluent_schema" "gateway_logs_value" {
  schema_registry_cluster {
    id = data.confluent_schema_registry_cluster.demo.id
  }
  rest_endpoint = data.confluent_schema_registry_cluster.demo.rest_endpoint
  subject_name  = "${local.topic}-value"
  format        = "AVRO"
  schema        = file("${path.module}/gateway-logs-value.avsc")

  credentials {
    key    = confluent_api_key.schema_registry.id
    secret = confluent_api_key.schema_registry.secret
  }

  hard_delete = true

  depends_on = [time_sleep.wait_for_keys]
}

resource "confluent_kafka_topic" "gateway_logs" {
  kafka_cluster {
    id = confluent_kafka_cluster.demo.id
  }
  topic_name       = local.topic
  partitions_count = var.partitions
  rest_endpoint    = confluent_kafka_cluster.demo.rest_endpoint

  credentials {
    key    = confluent_api_key.kafka.id
    secret = confluent_api_key.kafka.secret
  }

  depends_on = [confluent_schema.gateway_logs_value]
}
