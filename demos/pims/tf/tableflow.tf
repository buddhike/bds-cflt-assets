# Tableflow can only be enabled on a topic that has a schema, and Debezium
# registers a topic's schema with its first record. So the app starts first and
# makes the records, Terraform waits for the schemas, enables Tableflow, and then
# tells the app by setting an SSM parameter.

resource "terraform_data" "wait_for_schemas" {
  count = var.wait_for_schemas ? 1 : 0

  # Once the app is up. It installs the schema, registers the connector and starts
  # the simulator by itself.
  depends_on = [aws_ecs_service.pims]

  input = {
    schema_registry = data.confluent_schema_registry_cluster.pims.rest_endpoint
    topics          = join(",", sort(tolist(local.tables)))
  }

  provisioner "local-exec" {
    command = "python3 ${path.module}/scripts/wait_for_subjects.py"
    environment = {
      SCHEMA_REGISTRY_URL    = data.confluent_schema_registry_cluster.pims.rest_endpoint
      SCHEMA_REGISTRY_KEY    = confluent_api_key.schema_registry.id
      SCHEMA_REGISTRY_SECRET = confluent_api_key.schema_registry.secret
      TOPICS                 = join(",", sort(tolist(local.tables)))
      TIMEOUT_SECONDS        = "1500"
    }
  }
}

resource "confluent_tableflow_topic" "cdc" {
  for_each = local.tables

  environment {
    id = confluent_environment.pims.id
  }
  kafka_cluster {
    id = confluent_kafka_cluster.pims.id
  }
  display_name  = confluent_kafka_topic.cdc[each.key].topic_name
  table_formats = ["ICEBERG"]

  managed_storage {}

  credentials {
    key    = confluent_api_key.tableflow.id
    secret = confluent_api_key.tableflow.secret
  }

  depends_on = [terraform_data.wait_for_schemas]
}

# The app waits for this. Until it exists the app treats Tableflow as off.
resource "aws_ssm_parameter" "tableflow_enabled" {
  name  = local.tableflow_param_name
  type  = "String"
  value = "true"

  depends_on = [confluent_tableflow_topic.cdc]
}
