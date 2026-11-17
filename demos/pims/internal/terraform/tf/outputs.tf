output "organization_id" {
  value = data.confluent_organization.this.id
}

output "environment_id" {
  value = confluent_environment.pims.id
}

output "kafka_cluster_id" {
  value = confluent_kafka_cluster.pims.id
}

output "kafka_bootstrap" {
  # Strip the SASL_SSL:// scheme; Kafka clients want host:port.
  value = replace(confluent_kafka_cluster.pims.bootstrap_endpoint, "SASL_SSL://", "")
}

output "kafka_api_key" {
  value     = confluent_api_key.kafka.id
  sensitive = true
}

output "kafka_api_secret" {
  value     = confluent_api_key.kafka.secret
  sensitive = true
}

output "schema_registry_url" {
  value = data.confluent_schema_registry_cluster.pims.rest_endpoint
}

output "schema_registry_api_key" {
  value     = confluent_api_key.schema_registry.id
  sensitive = true
}

output "schema_registry_api_secret" {
  value     = confluent_api_key.schema_registry.secret
  sensitive = true
}

output "tableflow_api_key" {
  value     = confluent_api_key.tableflow.id
  sensitive = true
}

output "tableflow_api_secret" {
  value     = confluent_api_key.tableflow.secret
  sensitive = true
}

output "tableflow_catalog_endpoint" {
  value = "https://tableflow.${var.aws_region}.aws.confluent.cloud/iceberg/catalog/organizations/${data.confluent_organization.this.id}/environments/${confluent_environment.pims.id}"
}

output "topics" {
  value = sort([for t in confluent_kafka_topic.cdc : t.topic_name])
}

output "tableflow_enabled" {
  value = var.enable_tableflow
}
