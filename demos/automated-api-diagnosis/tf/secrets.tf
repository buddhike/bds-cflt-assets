# Everything secret reaches the container through one Secrets Manager secret, not
# through the task definition, which anyone who can describe the task reads.

# Opens the UI. It is in the link Launchpad shows, once.
resource "random_password" "access_token" {
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "demo" {
  name        = "${local.name}/config"
  description = "Credentials of the ${local.name} automated API diagnosis demo"
  # Deleted at once on destroy. Waiting out the recovery window would leave a
  # secret nobody asked to keep.
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "demo" {
  secret_id = aws_secretsmanager_secret.demo.id
  secret_string = jsonencode({
    ACCESS_TOKEN           = random_password.access_token.result
    KAFKA_API_KEY          = confluent_api_key.kafka.id
    KAFKA_API_SECRET       = confluent_api_key.kafka.secret
    SCHEMA_REGISTRY_KEY    = confluent_api_key.schema_registry.id
    SCHEMA_REGISTRY_SECRET = confluent_api_key.schema_registry.secret
  })
}
