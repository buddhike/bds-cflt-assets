# Everything secret reaches the containers through one Secrets Manager secret, not through
# the task definitions, which anyone who can describe a task reads. Each role is injected
# with only the keys it uses (ecs.tf). Terraform's own keys (the provisioner's) are not here.

# Opens the UI, and nothing else: there is no shutdown hook, so the token cannot tear anything
# down. Only the control task holds it. It is in the link Launchpad shows, once.
resource "random_password" "access_token" {
  length  = 32
  special = false
}

# What the producer and query tasks show the control service. It opens /internal/ and nothing
# else, so a worker that leaks it cannot open the UI.
resource "random_password" "worker_token" {
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "demo" {
  name        = "${local.name}/config"
  description = "Credentials of the ${local.name} Lightning inventory demo"
  # Deleted at once on destroy. Waiting out the recovery window would leave a
  # secret nobody asked to keep.
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "demo" {
  secret_id = aws_secretsmanager_secret.demo.id
  secret_string = jsonencode({
    ACCESS_TOKEN      = random_password.access_token.result
    WORKER_TOKEN      = random_password.worker_token.result
    KAFKA_API_KEY     = confluent_api_key.producer_kafka.id
    KAFKA_API_SECRET  = confluent_api_key.producer_kafka.secret
    GLOBAL_API_KEY    = confluent_api_key.query_global.id
    GLOBAL_API_SECRET = confluent_api_key.query_global.secret
    STATUS_API_KEY    = confluent_api_key.status_global.id
    STATUS_API_SECRET = confluent_api_key.status_global.secret
    CLOUD_API_KEY     = confluent_api_key.control_cloud.id
    CLOUD_API_SECRET  = confluent_api_key.control_cloud.secret
  })
}
