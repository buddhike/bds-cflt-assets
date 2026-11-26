# One Fargate task runs the four containers, so they share a network namespace
# and reach each other on 127.0.0.1:
#
#   pims-server  :8080  the simulator, the UI and its API; the only port the load balancer reaches
#   mysql        :3306  the clinic's database, with the binlog Debezium reads
#   connect      :8083  Kafka Connect with the Debezium MySQL plugin
#   duckdb       :8081  DuckDB CLI and the agent that runs the UI's queries

data "aws_partition" "current" {}

locals {
  secret_arn = aws_secretsmanager_secret.pims.arn
  # A secret's JSON key as a container secret: <arn>:<key>::
  from_secret = { for k in [
    "ACCESS_TOKEN", "AGENT_TOKEN", "CC_API_KEY", "CC_API_SECRET",
    "MYSQL_ROOT_PASSWORD", "MYSQL_APP_PASSWORD", "MYSQL_DEBEZIUM_PASSWORD",
    "KAFKA_API_KEY", "KAFKA_API_SECRET", "KAFKA_JAAS", "SR_API_KEY", "SR_API_SECRET", "SR_USER_INFO",
    "TABLEFLOW_API_KEY", "TABLEFLOW_API_SECRET",
  ] : k => "${local.secret_arn}:${k}::" }

  # Private packages need the registry's credentials. The MySQL image is from the
  # public ECR mirror of Docker Hub, which does not rate limit Fargate.
  registry_auth = var.registry_credentials_secret_arn == null ? {} : {
    repositoryCredentials = { credentialsParameter = var.registry_credentials_secret_arn }
  }

  kafka_bootstrap = replace(confluent_kafka_cluster.pims.bootstrap_endpoint, "SASL_SSL://", "")

  log_options = { for name in ["pims-server", "mysql", "connect", "duckdb"] : name => {
    logDriver = "awslogs"
    options = {
      awslogs-group         = aws_cloudwatch_log_group.pims.name
      awslogs-region        = var.aws_region
      awslogs-stream-prefix = name
    }
  } }

  # Kafka Connect, configured as the desktop app's compose file does.
  connect_env = [
    { name = "CONNECT_BOOTSTRAP_SERVERS", value = local.kafka_bootstrap },
    { name = "CONNECT_GROUP_ID", value = "pims-connect" },
    { name = "CONNECT_REST_ADVERTISED_HOST_NAME", value = "127.0.0.1" },
    { name = "CONNECT_PLUGIN_PATH", value = "/usr/share/java,/usr/share/confluent-hub-components" },
    # The internal topics live in Confluent Cloud, which requires a replication factor of 3.
    { name = "CONNECT_CONFIG_STORAGE_TOPIC", value = "_pims-connect-configs" },
    { name = "CONNECT_OFFSET_STORAGE_TOPIC", value = "_pims-connect-offsets" },
    { name = "CONNECT_STATUS_STORAGE_TOPIC", value = "_pims-connect-status" },
    { name = "CONNECT_CONFIG_STORAGE_REPLICATION_FACTOR", value = "3" },
    { name = "CONNECT_OFFSET_STORAGE_REPLICATION_FACTOR", value = "3" },
    { name = "CONNECT_STATUS_STORAGE_REPLICATION_FACTOR", value = "3" },
    # The worker, its producer and its consumer all sign in to Confluent Cloud.
    { name = "CONNECT_SECURITY_PROTOCOL", value = "SASL_SSL" },
    { name = "CONNECT_SASL_MECHANISM", value = "PLAIN" },
    { name = "CONNECT_SSL_ENDPOINT_IDENTIFICATION_ALGORITHM", value = "https" },
    { name = "CONNECT_PRODUCER_SECURITY_PROTOCOL", value = "SASL_SSL" },
    { name = "CONNECT_PRODUCER_SASL_MECHANISM", value = "PLAIN" },
    { name = "CONNECT_CONSUMER_SECURITY_PROTOCOL", value = "SASL_SSL" },
    { name = "CONNECT_CONSUMER_SASL_MECHANISM", value = "PLAIN" },
    # Avro keeps schemas in Schema Registry, which Tableflow requires.
    { name = "CONNECT_KEY_CONVERTER", value = "io.confluent.connect.avro.AvroConverter" },
    { name = "CONNECT_KEY_CONVERTER_SCHEMA_REGISTRY_URL", value = data.confluent_schema_registry_cluster.pims.rest_endpoint },
    { name = "CONNECT_KEY_CONVERTER_BASIC_AUTH_CREDENTIALS_SOURCE", value = "USER_INFO" },
    { name = "CONNECT_VALUE_CONVERTER", value = "io.confluent.connect.avro.AvroConverter" },
    { name = "CONNECT_VALUE_CONVERTER_SCHEMA_REGISTRY_URL", value = data.confluent_schema_registry_cluster.pims.rest_endpoint },
    { name = "CONNECT_VALUE_CONVERTER_BASIC_AUTH_CREDENTIALS_SOURCE", value = "USER_INFO" },
    # Connector configs say ${env:...} and Connect resolves them here, so secrets are
    # never posted to, or returned by, its REST API.
    { name = "CONNECT_CONFIG_PROVIDERS", value = "env" },
    { name = "CONNECT_CONFIG_PROVIDERS_ENV_CLASS", value = "org.apache.kafka.common.config.provider.EnvVarConfigProvider" },
    # Leaves room in the task for MySQL and DuckDB.
    { name = "KAFKA_HEAP_OPTS", value = "-Xms512m -Xmx1536m" },
  ]
  connect_secrets = [
    { name = "CONNECT_SASL_JAAS_CONFIG", valueFrom = local.from_secret["KAFKA_JAAS"] },
    { name = "CONNECT_PRODUCER_SASL_JAAS_CONFIG", valueFrom = local.from_secret["KAFKA_JAAS"] },
    { name = "CONNECT_CONSUMER_SASL_JAAS_CONFIG", valueFrom = local.from_secret["KAFKA_JAAS"] },
    { name = "CONNECT_KEY_CONVERTER_SCHEMA_REGISTRY_BASIC_AUTH_USER_INFO", valueFrom = local.from_secret["SR_USER_INFO"] },
    { name = "CONNECT_VALUE_CONVERTER_SCHEMA_REGISTRY_BASIC_AUTH_USER_INFO", valueFrom = local.from_secret["SR_USER_INFO"] },
    { name = "PIMS_KAFKA_API_KEY", valueFrom = local.from_secret["KAFKA_API_KEY"] },
    { name = "PIMS_KAFKA_API_SECRET", valueFrom = local.from_secret["KAFKA_API_SECRET"] },
    { name = "PIMS_MYSQL_DEBEZIUM_PASSWORD", valueFrom = local.from_secret["MYSQL_DEBEZIUM_PASSWORD"] },
  ]

  # The contract in docs/specs/launchpad.md.
  app_env = [
    { name = "PIMS_AWS_REGION", value = var.aws_region },
    { name = "PIMS_ORGANIZATION_ID", value = data.confluent_organization.this.id },
    { name = "PIMS_ENVIRONMENT_ID", value = confluent_environment.pims.id },
    { name = "PIMS_KAFKA_CLUSTER_ID", value = confluent_kafka_cluster.pims.id },
    { name = "PIMS_KAFKA_BOOTSTRAP", value = local.kafka_bootstrap },
    { name = "PIMS_SCHEMA_REGISTRY_URL", value = data.confluent_schema_registry_cluster.pims.rest_endpoint },
    { name = "PIMS_TABLEFLOW_CATALOG_ENDPOINT", value = "https://tableflow.${var.aws_region}.aws.confluent.cloud/iceberg/catalog/organizations/${data.confluent_organization.this.id}/environments/${confluent_environment.pims.id}" },
    { name = "PIMS_TOPICS", value = join(",", sort(tolist(local.tables))) },
  ]
  app_secrets = [
    { name = "PIMS_ACCESS_TOKEN", valueFrom = local.from_secret["ACCESS_TOKEN"] },
    { name = "PIMS_AGENT_TOKEN", valueFrom = local.from_secret["AGENT_TOKEN"] },
    { name = "PIMS_CC_API_KEY", valueFrom = local.from_secret["CC_API_KEY"] },
    { name = "PIMS_CC_API_SECRET", valueFrom = local.from_secret["CC_API_SECRET"] },
    { name = "PIMS_MYSQL_ROOT_PASSWORD", valueFrom = local.from_secret["MYSQL_ROOT_PASSWORD"] },
    { name = "PIMS_MYSQL_APP_PASSWORD", valueFrom = local.from_secret["MYSQL_APP_PASSWORD"] },
    { name = "PIMS_MYSQL_DEBEZIUM_PASSWORD", valueFrom = local.from_secret["MYSQL_DEBEZIUM_PASSWORD"] },
    { name = "PIMS_KAFKA_API_KEY", valueFrom = local.from_secret["KAFKA_API_KEY"] },
    { name = "PIMS_KAFKA_API_SECRET", valueFrom = local.from_secret["KAFKA_API_SECRET"] },
    { name = "PIMS_SCHEMA_REGISTRY_API_KEY", valueFrom = local.from_secret["SR_API_KEY"] },
    { name = "PIMS_SCHEMA_REGISTRY_API_SECRET", valueFrom = local.from_secret["SR_API_SECRET"] },
    { name = "PIMS_TABLEFLOW_API_KEY", valueFrom = local.from_secret["TABLEFLOW_API_KEY"] },
    { name = "PIMS_TABLEFLOW_API_SECRET", valueFrom = local.from_secret["TABLEFLOW_API_SECRET"] },
  ]
}

resource "aws_cloudwatch_log_group" "pims" {
  name              = "/pims/${local.name}"
  retention_in_days = 14
}

data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# The execution role is ECS's own: it pulls images and injects the secrets.
resource "aws_iam_role" "execution" {
  name               = "${local.name}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "execution_secrets" {
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = compact([local.secret_arn, var.registry_credentials_secret_arn])
  }
}

resource "aws_iam_role_policy" "execution_secrets" {
  name   = "secrets"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.execution_secrets.json
}

resource "aws_ecs_cluster" "pims" {
  name = local.name
}

resource "aws_ecs_task_definition" "pims" {
  family                   = local.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.task_cpu
  memory                   = var.task_memory
  execution_role_arn       = aws_iam_role.execution.arn

  # MySQL's data and DuckDB's spill space.
  ephemeral_storage {
    size_in_gib = 30
  }

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([
    {
      name      = "mysql"
      image     = "public.ecr.aws/docker/library/mysql:8.4"
      essential = true
      # Debezium reads the binlog: row-based, full row images, GTIDs on. Times are
      # stored and emitted in UTC so they are unambiguous.
      command = [
        "--server-id=1", "--log-bin=mysql-bin", "--binlog-format=ROW", "--binlog-row-image=FULL",
        "--gtid-mode=ON", "--enforce-gtid-consistency=ON", "--default-time-zone=+00:00",
      ]
      environment = [
        { name = "MYSQL_DATABASE", value = "pims" },
        { name = "MYSQL_USER", value = "pims" },
      ]
      secrets = [
        { name = "MYSQL_ROOT_PASSWORD", valueFrom = local.from_secret["MYSQL_ROOT_PASSWORD"] },
        { name = "MYSQL_PASSWORD", valueFrom = local.from_secret["MYSQL_APP_PASSWORD"] },
      ]
      memoryReservation = 1024
      healthCheck = {
        # ping succeeds even when access is denied, so it needs no password.
        command = ["CMD", "mysqladmin", "ping", "-h", "127.0.0.1", "--silent"]
        # ECS allows at most 10 retries, so the 150 s of patience comes from the interval.
        interval    = 15
        timeout     = 5
        retries     = 10
        startPeriod = 30
      }
      logConfiguration = local.log_options["mysql"]
    },
    merge({
      name        = "connect"
      image       = "${var.image_registry}/pims-connect:${var.image_tag}"
      essential   = true
      environment = local.connect_env
      secrets     = local.connect_secrets
      dependsOn   = [{ containerName = "mysql", condition = "HEALTHY" }]
      # Heap 1.5 GB plus the JVM's own and the plugins'.
      memoryReservation = 2560
      healthCheck = {
        command = ["CMD-SHELL", "curl -fsS http://localhost:8083/connectors > /dev/null"]
        # ECS allows at most 10 retries, so the 300 s of patience comes from the interval.
        interval    = 30
        timeout     = 5
        retries     = 10
        startPeriod = 120
      }
      logConfiguration = local.log_options["connect"]
    }, local.registry_auth),
    merge({
      name  = "duckdb"
      image = "${var.image_registry}/pims-duckdb:${var.image_tag}"
      # Queries fail if it stops, but a restart of the task would drop MySQL's data,
      # so it does not take the task with it.
      essential         = false
      secrets           = [{ name = "PIMS_AGENT_TOKEN", valueFrom = local.from_secret["AGENT_TOKEN"] }]
      memoryReservation = 2048
      logConfiguration  = local.log_options["duckdb"]
    }, local.registry_auth),
    merge({
      name              = "pims-server"
      image             = "${var.image_registry}/pims-server:${var.image_tag}"
      essential         = true
      portMappings      = [{ containerPort = 8080, protocol = "tcp" }]
      environment       = local.app_env
      secrets           = local.app_secrets
      memoryReservation = 512
      logConfiguration  = local.log_options["pims-server"]
    }, local.registry_auth),
  ])
}

resource "aws_ecs_service" "pims" {
  name            = local.name
  cluster         = aws_ecs_cluster.pims.id
  task_definition = aws_ecs_task_definition.pims.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  # Never two tasks: a second MySQL and a second connector would split the demo.
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
  health_check_grace_period_seconds  = 300

  # A task that cannot start, for example because an image is private, fails the
  # apply instead of retrying until the wait times out.
  deployment_circuit_breaker {
    enable   = true
    rollback = false
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.task.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.app.arn
    container_name   = "pims-server"
    container_port   = 8080
  }

  # Terraform goes on to wait for the schemas once the load balancer sees the app.
  wait_for_steady_state = true

  depends_on = [aws_lb_listener.http, aws_secretsmanager_secret_version.pims, aws_iam_role_policy.execution_secrets]
}
