# Flink: finds the unhealthy APIs in the gateway logs.
#
#   gateway-logs ──► detect_anomalies.sql ──► api_unhealthy_events ──► the monitor
#                                                      │
#                                   (optional, ai.tf)  └─► an AI agent ──► api_anomaly_ai_reports ──► the AI reports tab
#
# The statements are in flink/, run here as the demo's service account.

data "confluent_flink_region" "demo" {
  cloud  = "AWS"
  region = var.aws_region
}

resource "confluent_flink_compute_pool" "demo" {
  display_name = local.name
  cloud        = "AWS"
  region       = var.aws_region
  max_cfu      = var.flink_max_cfu

  environment {
    id = confluent_environment.demo.id
  }
}

# The key Flink statements are submitted with.
resource "confluent_api_key" "flink" {
  display_name = "${local.name}-flink"
  description  = "Submits the demo's Flink statements"

  owner {
    id          = confluent_service_account.demo.id
    api_version = confluent_service_account.demo.api_version
    kind        = confluent_service_account.demo.kind
  }

  managed_resource {
    id          = data.confluent_flink_region.demo.id
    api_version = data.confluent_flink_region.demo.api_version
    kind        = data.confluent_flink_region.demo.kind

    environment {
      id = confluent_environment.demo.id
    }
  }

  depends_on = [confluent_role_binding.flink_admin]
}

locals {
  # Unqualified table names in the statements resolve in this environment and cluster.
  flink_properties = {
    "sql.current-catalog"  = confluent_environment.demo.display_name
    "sql.current-database" = confluent_kafka_cluster.demo.display_name
  }
}

# The logs' own timestamp, not the time Kafka received a record, is the event time the
# windows use. The field is already a TIMESTAMP(3), which Flink allows as a time attribute.
resource "confluent_flink_statement" "alter_watermark" {
  statement_name = "${local.name}-alter-watermark"
  statement      = file("${path.module}/flink/alter_watermark.sql")
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

  depends_on = [confluent_kafka_topic.gateway_logs, confluent_schema.gateway_logs_value]
}

# Creating a table creates its topic and registers its schemas, so the monitor can read
# the topic as soon as this statement is done.
resource "confluent_flink_statement" "create_unhealthy_events" {
  statement_name = "${local.name}-create-unhealthy-events"
  statement      = file("${path.module}/flink/create_unhealthy_events.sql")
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

  depends_on = [confluent_flink_statement.alter_watermark]
}

# The table the AI agent's reports go to, in the shape the app reads (internal/reports).
# It exists whether or not an agent is deployed, so the app's reader always has a topic.
resource "confluent_flink_statement" "create_anomaly_reports" {
  statement_name = "${local.name}-create-anomaly-reports"
  statement      = file("${path.module}/flink/create_anomaly_reports.sql")
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

  depends_on = [confluent_flink_statement.create_unhealthy_events]
}

# Runs for as long as the demo does. It starts from the first record, not the latest, so
# it learns each API's normal error rate from everything the simulator has sent: the
# detector needs 30 ten-second windows (five minutes) of history before it scores.
resource "confluent_flink_statement" "detect_anomalies" {
  statement_name = "${local.name}-detect-anomalies"
  statement      = file("${path.module}/flink/detect_anomalies.sql")
  properties = merge(local.flink_properties, {
    "sql.tables.scan.startup.mode" = "earliest-offset"
  })

  compute_pool { id = confluent_flink_compute_pool.demo.id }
  principal { id = confluent_service_account.demo.id }
  environment { id = confluent_environment.demo.id }
  organization { id = data.confluent_organization.this.id }
  credentials {
    key    = confluent_api_key.flink.id
    secret = confluent_api_key.flink.secret
  }
  rest_endpoint = data.confluent_flink_region.demo.rest_endpoint

  depends_on = [confluent_flink_statement.create_unhealthy_events]
}
