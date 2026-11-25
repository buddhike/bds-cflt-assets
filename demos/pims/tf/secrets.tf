# Everything secret reaches the containers through one Secrets Manager secret,
# not through the task definition, which anyone who can describe the task reads.

resource "random_password" "mysql_root" {
  length  = 24
  special = false
}

resource "random_password" "mysql_app" {
  length  = 24
  special = false
}

resource "random_password" "mysql_debezium" {
  length  = 24
  special = false
}

# Opens the UI. It is in the link Launchpad shows, once.
resource "random_password" "access_token" {
  length  = 32
  special = false
}

# Lets the app, and nothing else, run commands in the DuckDB container.
resource "random_password" "agent_token" {
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "pims" {
  name        = "${local.name}/config"
  description = "Credentials of the ${local.name} PIMS demo"
  # Deleted at once on destroy. Waiting out the recovery window would leave a
  # secret nobody asked to keep.
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "pims" {
  secret_id = aws_secretsmanager_secret.pims.id
  secret_string = jsonencode({
    ACCESS_TOKEN            = random_password.access_token.result
    AGENT_TOKEN             = random_password.agent_token.result
    CC_API_KEY              = var.confluent_cloud_api_key
    CC_API_SECRET           = var.confluent_cloud_api_secret
    MYSQL_ROOT_PASSWORD     = random_password.mysql_root.result
    MYSQL_APP_PASSWORD      = random_password.mysql_app.result
    MYSQL_DEBEZIUM_PASSWORD = random_password.mysql_debezium.result
    KAFKA_API_KEY           = confluent_api_key.kafka.id
    KAFKA_API_SECRET        = confluent_api_key.kafka.secret
    KAFKA_JAAS              = "org.apache.kafka.common.security.plain.PlainLoginModule required username=\"${confluent_api_key.kafka.id}\" password=\"${confluent_api_key.kafka.secret}\";"
    SR_API_KEY              = confluent_api_key.schema_registry.id
    SR_API_SECRET           = confluent_api_key.schema_registry.secret
    SR_USER_INFO            = "${confluent_api_key.schema_registry.id}:${confluent_api_key.schema_registry.secret}"
    TABLEFLOW_API_KEY       = confluent_api_key.tableflow.id
    TABLEFLOW_API_SECRET    = confluent_api_key.tableflow.secret
  })
}
