# Confluent Cloud: the environment, the Standard cluster, the topic with its schema, the
# Lightning Table (confluent_rtce_topic) and the service accounts and keys the app uses.
# Terraform owns all of it, so teardown is `terraform destroy` and there is no shutdown hook
# (docs/specs/launchpad.md, "Decision on who enables the table"). contract_test.go checks that
# the table is declared here and that no hook exists.

locals {
  # The topic. Lightning Table topic names are letters, digits and underscores only, so no
  # hyphen. The app's default (config.DefaultTopic) is the same name, which contract_test.go
  # checks.
  topic = "inventory_positions"

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
  cloud        = "AWS" # Lightning Tables are available on AWS only
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

# Five service accounts, so that no task holds more than it uses. All are scoped to this
# demo's environment, which holds nothing else.
#
#   provisioner  creates the topic and the schema. Its keys are used by Terraform only: they
#                are not in Secrets Manager and no task ever sees them. EnvironmentAdmin is the
#                one broad role, and it is accepted (docs/specs/launchpad.md, "Accepted risks").
#   producer     DeveloperWrite on the topic.
#   query        DeveloperRead on the topic and on Schema Registry.
#   control      MetricsViewer on the cluster: it reads Confluent's usage metrics and
#                changes nothing.
#   status       CloudClusterAdmin on the cluster: the control task reads the table's status
#                with its Global key, and never writes (the client only GETs).
#
# The Lightning Table itself is created by the provider's own credentials (the launching
# user's Cloud API key), not by any of these.
resource "confluent_service_account" "provisioner" {
  display_name = "${local.name}-provisioner-${confluent_environment.demo.id}"
  description  = "Lightning inventory demo: Terraform creating the topic and the schema"
}

resource "confluent_service_account" "producer" {
  display_name = "${local.name}-producer-${confluent_environment.demo.id}"
  description  = "Lightning inventory demo: the producer tasks"
}

resource "confluent_service_account" "query" {
  display_name = "${local.name}-query-${confluent_environment.demo.id}"
  description  = "Lightning inventory demo: the query tasks"
}

resource "confluent_service_account" "control" {
  display_name = "${local.name}-control-${confluent_environment.demo.id}"
  description  = "Lightning inventory demo: the control task, read-only"
}

resource "confluent_service_account" "status" {
  display_name = "${local.name}-status-${confluent_environment.demo.id}"
  description  = "Lightning inventory demo: the control task reading the table's status"
}

resource "confluent_role_binding" "provisioner" {
  principal   = "User:${confluent_service_account.provisioner.id}"
  role_name   = "EnvironmentAdmin"
  crn_pattern = confluent_environment.demo.resource_name
}

# The CRN of the topic, for the roles scoped to it.
locals {
  topic_crn = "${confluent_kafka_cluster.demo.rbac_crn}/kafka=${confluent_kafka_cluster.demo.id}/topic=${confluent_kafka_topic.inventory.topic_name}"
}

resource "confluent_role_binding" "producer_write" {
  principal   = "User:${confluent_service_account.producer.id}"
  role_name   = "DeveloperWrite"
  crn_pattern = local.topic_crn
}

resource "confluent_role_binding" "query_read" {
  principal   = "User:${confluent_service_account.query.id}"
  role_name   = "DeveloperRead"
  crn_pattern = local.topic_crn
}

# The query docs name DeveloperRead on the Schema Registry cluster as well. This demo's
# registry holds one schema, so every subject is that one.
resource "confluent_role_binding" "query_schema_read" {
  principal   = "User:${confluent_service_account.query.id}"
  role_name   = "DeveloperRead"
  crn_pattern = "${data.confluent_schema_registry_cluster.demo.resource_name}/subject=*"
}

# Reading the table's status (the list, GET /rtce/v1/rtce-topics; the by-name read is refused) answered 403 with DeveloperRead on
# the topic, or on every topic of the cluster, and works with CloudClusterAdmin on the cluster
# (found against a live account; the documents name no role for the read). It is the only
# administrator role a task holds, and it is held by the status account alone. Its key is a
# Global key, which the Lightning Tables documents accept; whether that kind of key can also
# reach other management APIs is not documented here, so treat the key as an administrator's.
resource "confluent_role_binding" "status_cluster_admin" {
  principal   = "User:${confluent_service_account.status.id}"
  role_name   = "CloudClusterAdmin"
  crn_pattern = confluent_kafka_cluster.demo.rbac_crn
}

# The control task reads Confluent's usage metrics for the table with this role, on the
# cluster only.
resource "confluent_role_binding" "control_metrics" {
  principal   = "User:${confluent_service_account.control.id}"
  role_name   = "MetricsViewer"
  crn_pattern = confluent_kafka_cluster.demo.rbac_crn
}

# Terraform's keys, for the topic and the schema. Nothing else uses them.
resource "confluent_api_key" "provisioner_kafka" {
  display_name = "${local.name}-provisioner-kafka"
  description  = "Terraform creating the topic. Used by Terraform only"

  owner {
    id          = confluent_service_account.provisioner.id
    api_version = confluent_service_account.provisioner.api_version
    kind        = confluent_service_account.provisioner.kind
  }

  managed_resource {
    id          = confluent_kafka_cluster.demo.id
    api_version = confluent_kafka_cluster.demo.api_version
    kind        = confluent_kafka_cluster.demo.kind

    environment {
      id = confluent_environment.demo.id
    }
  }

  depends_on = [confluent_role_binding.provisioner]
}

resource "confluent_api_key" "provisioner_schema_registry" {
  display_name = "${local.name}-provisioner-schema-registry"
  description  = "Terraform registering the schema. Used by Terraform only"

  owner {
    id          = confluent_service_account.provisioner.id
    api_version = confluent_service_account.provisioner.api_version
    kind        = confluent_service_account.provisioner.kind
  }

  managed_resource {
    id          = data.confluent_schema_registry_cluster.demo.id
    api_version = data.confluent_schema_registry_cluster.demo.api_version
    kind        = data.confluent_schema_registry_cluster.demo.kind

    environment {
      id = confluent_environment.demo.id
    }
  }

  depends_on = [confluent_role_binding.provisioner]
}

# The producers' key. A Standard cluster takes cluster-scoped Kafka keys.
resource "confluent_api_key" "producer_kafka" {
  display_name = "${local.name}-producer-kafka"
  description  = "The producer tasks producing to the topic"

  owner {
    id          = confluent_service_account.producer.id
    api_version = confluent_service_account.producer.api_version
    kind        = confluent_service_account.producer.kind
  }

  managed_resource {
    id          = confluent_kafka_cluster.demo.id
    api_version = confluent_kafka_cluster.demo.api_version
    kind        = confluent_kafka_cluster.demo.kind

    environment {
      id = confluent_environment.demo.id
    }
  }

  depends_on = [confluent_role_binding.producer_write]
}

# A Global API key: the one credential the Lightning Table's query endpoint accepts
# (docs.confluent.io/cloud/current/lightning-tables/query/api.html). It grants nothing by
# itself: it has the roles of the query service account, which are read-only.
resource "confluent_api_key" "query_global" {
  display_name = "${local.name}-query-global"
  description  = "The query tasks querying the Lightning Table"

  owner {
    id          = confluent_service_account.query.id
    api_version = confluent_service_account.query.api_version
    kind        = confluent_service_account.query.kind
  }

  managed_resource {
    id          = "global"
    api_version = "global/v1"
    kind        = "Global"
  }

  depends_on = [confluent_role_binding.query_read, confluent_role_binding.query_schema_read]
}

# A Global API key to read the table's status. It grants nothing by itself: it has the roles
# of the status service account.
resource "confluent_api_key" "status_global" {
  display_name = "${local.name}-status-global"
  description  = "The control task reading the table's status"

  owner {
    id          = confluent_service_account.status.id
    api_version = confluent_service_account.status.api_version
    kind        = confluent_service_account.status.kind
  }

  managed_resource {
    id          = "global"
    api_version = "global/v1"
    kind        = "Global"
  }

  depends_on = [confluent_role_binding.status_cluster_admin]
}

# A Cloud API key, for the Metrics API (which takes no other kind). It has the roles of the
# control service account, which change nothing.
resource "confluent_api_key" "control_cloud" {
  display_name = "${local.name}-control-cloud"
  description  = "The control task reading Confluent's usage metrics"

  owner {
    id          = confluent_service_account.control.id
    api_version = confluent_service_account.control.api_version
    kind        = confluent_service_account.control.kind
  }

  depends_on = [confluent_role_binding.control_metrics]
}

# A key is not always usable the moment its create call returns. Using it at once, to
# register the schema, can fail with 401 Unauthorized, which is what Confluent's own examples
# wait for.
resource "time_sleep" "wait_for_provisioner_keys" {
  depends_on      = [confluent_api_key.provisioner_schema_registry, confluent_api_key.provisioner_kafka]
  create_duration = "30s"
}

# The same wait for the keys the tasks use, which are created after the topic (their roles
# are scoped to it). The control and worker services start after it.
resource "time_sleep" "wait_for_app_keys" {
  depends_on      = [confluent_api_key.producer_kafka, confluent_api_key.query_global, confluent_api_key.status_global, confluent_api_key.control_cloud]
  create_duration = "30s"
}

# The schema comes before the topic: a Lightning Table needs a registered schema, and the
# topic then never exists without one.
#
# inventory-value.avsc is the schema the Go producer encodes with (worker.SchemaJSON). A test
# (contract_test.go) keeps the two the same. All fields are required, so the table's columns
# are NOT NULL.
resource "confluent_schema" "inventory_value" {
  schema_registry_cluster {
    id = data.confluent_schema_registry_cluster.demo.id
  }
  rest_endpoint = data.confluent_schema_registry_cluster.demo.rest_endpoint
  subject_name  = "${local.topic}-value"
  format        = "AVRO"
  schema        = file("${path.module}/inventory-value.avsc")

  credentials {
    key    = confluent_api_key.provisioner_schema_registry.id
    secret = confluent_api_key.provisioner_schema_registry.secret
  }

  hard_delete = true

  depends_on = [time_sleep.wait_for_provisioner_keys]
}

# Compacted, so the Lightning Table runs in upsert mode and keeps the latest quantity for each
# key (the record key is "<sku>|<location_id>": one key per pair). 6 partitions.
#
# Changing partitions_count replaces the topic, which loses the data and, because the Lightning
# Table depends on the topic, replaces the table too.
resource "confluent_kafka_topic" "inventory" {
  kafka_cluster {
    id = confluent_kafka_cluster.demo.id
  }
  topic_name       = local.topic
  partitions_count = var.partitions
  rest_endpoint    = confluent_kafka_cluster.demo.rest_endpoint

  config = {
    "cleanup.policy" = "compact"
  }

  credentials {
    key    = confluent_api_key.provisioner_kafka.id
    secret = confluent_api_key.provisioner_kafka.secret
  }

  depends_on = [confluent_schema.inventory_value]
}

# The Lightning Table. Terraform's own provider credentials (the launching user's Cloud API
# key) create it: none of the service accounts above can. It needs the topic and its schema.
# Destroy runs the other way round: the workers stop (they depend on it), the table goes, a
# pause (below), then the topic and the schema.
#
# Whether Terraform waits for the table to be ACTIVE is not documented, so the control task
# reads the phase and the UI does not allow a load until it is ACTIVE.
resource "confluent_rtce_topic" "inventory" {
  cloud       = "AWS"
  region      = var.aws_region
  topic_name  = confluent_kafka_topic.inventory.topic_name
  description = "Inventory positions: the latest quantity of a SKU at a location. Columns sku, location_id and quantity."

  environment {
    id = confluent_environment.demo.id
  }

  kafka_cluster {
    id = confluent_kafka_cluster.demo.id
  }

  # Enabling and disabling take "several minutes" (Confluent's docs give no figure).
  timeouts {
    create = "30m"
    delete = "30m"
  }

  depends_on = [confluent_schema.inventory_value, time_sleep.topic_settle]
}

# A topic that still backs a table may refuse to be deleted, and nothing documents whether
# deleting the table has finished by the time the provider returns. On destroy this waits a
# minute after the table is gone and before the topic goes, so teardown needs no one to retry.
resource "time_sleep" "topic_settle" {
  destroy_duration = "60s"

  depends_on = [confluent_kafka_topic.inventory]
}
