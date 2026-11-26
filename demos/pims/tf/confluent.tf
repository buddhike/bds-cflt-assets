# Confluent Cloud: the same resources the desktop app provisions, except for
# Tableflow. It cannot be enabled until Debezium has registered the topics' schemas,
# so pims-server enables it, and disables it again before a destroy.

locals {
  # Debezium routes each MySQL table to the topic of the same name.
  tables = toset(["patients", "visits", "appointments"])

  # Environment and service account names are unique within a Confluent Cloud
  # organisation, so a random suffix lets several people run the demo in one.
  name = "${var.launchpad_demo_id}-${random_id.suffix.hex}"
}

resource "random_id" "suffix" {
  byte_length = 3
}

data "confluent_organization" "this" {}

resource "confluent_environment" "pims" {
  display_name = local.name

  stream_governance {
    package = "ESSENTIALS"
  }
}

resource "confluent_kafka_cluster" "pims" {
  display_name = local.name
  availability = "SINGLE_ZONE"
  cloud        = "AWS"
  region       = var.aws_region
  standard {}

  environment {
    id = confluent_environment.pims.id
  }
}

# Schema Registry is provisioned automatically with the first cluster.
data "confluent_schema_registry_cluster" "pims" {
  environment {
    id = confluent_environment.pims.id
  }
  depends_on = [confluent_kafka_cluster.pims]
}

# One service account owns every key the app uses. It is scoped to the demo's
# environment, so it cannot touch anything else in the organisation.
resource "confluent_service_account" "pims" {
  display_name = "${local.name}-${confluent_environment.pims.id}"
  description  = "PIMS demo: Debezium, Schema Registry and Tableflow access"
}

resource "confluent_role_binding" "pims_env_admin" {
  principal   = "User:${confluent_service_account.pims.id}"
  role_name   = "EnvironmentAdmin"
  crn_pattern = confluent_environment.pims.resource_name
}

resource "confluent_api_key" "kafka" {
  display_name = "${local.name}-kafka"
  description  = "PIMS Kafka Connect / Debezium"

  owner {
    id          = confluent_service_account.pims.id
    api_version = confluent_service_account.pims.api_version
    kind        = confluent_service_account.pims.kind
  }

  managed_resource {
    id          = confluent_kafka_cluster.pims.id
    api_version = confluent_kafka_cluster.pims.api_version
    kind        = confluent_kafka_cluster.pims.kind

    environment {
      id = confluent_environment.pims.id
    }
  }

  depends_on = [confluent_role_binding.pims_env_admin]
}

resource "confluent_api_key" "schema_registry" {
  display_name = "${local.name}-schema-registry"
  description  = "PIMS Avro converter access to Schema Registry"

  owner {
    id          = confluent_service_account.pims.id
    api_version = confluent_service_account.pims.api_version
    kind        = confluent_service_account.pims.kind
  }

  managed_resource {
    id          = data.confluent_schema_registry_cluster.pims.id
    api_version = data.confluent_schema_registry_cluster.pims.api_version
    kind        = data.confluent_schema_registry_cluster.pims.kind

    environment {
      id = confluent_environment.pims.id
    }
  }

  depends_on = [confluent_role_binding.pims_env_admin]
}

resource "confluent_api_key" "tableflow" {
  display_name = "${local.name}-tableflow"
  description  = "PIMS Tableflow and Iceberg REST catalog access"

  owner {
    id          = confluent_service_account.pims.id
    api_version = confluent_service_account.pims.api_version
    kind        = confluent_service_account.pims.kind
  }

  managed_resource {
    id          = "tableflow"
    api_version = "tableflow/v1"
    kind        = "Tableflow"
  }

  depends_on = [confluent_role_binding.pims_env_admin]
}

# Tableflow's native Debezium support requires compacted topics: it puts the
# table in upsert mode keyed on the MySQL primary key.
resource "confluent_kafka_topic" "cdc" {
  for_each = local.tables

  kafka_cluster {
    id = confluent_kafka_cluster.pims.id
  }
  topic_name       = each.key
  partitions_count = var.partitions
  rest_endpoint    = confluent_kafka_cluster.pims.rest_endpoint

  config = {
    "cleanup.policy" = "compact"
  }

  credentials {
    key    = confluent_api_key.kafka.id
    secret = confluent_api_key.kafka.secret
  }
}
