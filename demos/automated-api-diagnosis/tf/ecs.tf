# One Fargate task runs the app: the simulator, the monitor and the UI, in one Go binary.
# The load balancer reaches its port 8080, and nothing else does.

data "aws_partition" "current" {}

locals {
  secret_arn = aws_secretsmanager_secret.demo.arn
  # A secret's JSON key as a container secret: <arn>:<key>::
  from_secret = { for k in [
    "ACCESS_TOKEN", "KAFKA_API_KEY", "KAFKA_API_SECRET", "SCHEMA_REGISTRY_KEY", "SCHEMA_REGISTRY_SECRET",
  ] : k => "${local.secret_arn}:${k}::" }

  # A private package needs the registry's credentials.
  registry_auth = var.registry_credentials_secret_arn == null ? {} : {
    repositoryCredentials = { credentialsParameter = var.registry_credentials_secret_arn }
  }

  # The contract in docs/specs/launchpad.md.
  app_env = [
    { name = "APIDIAG_AWS_REGION", value = var.aws_region },
    { name = "APIDIAG_DEMO_NAME", value = local.name },
    { name = "APIDIAG_ENVIRONMENT_ID", value = confluent_environment.demo.id },
    { name = "APIDIAG_KAFKA_CLUSTER_ID", value = confluent_kafka_cluster.demo.id },
    { name = "APIDIAG_KAFKA_BOOTSTRAP", value = replace(confluent_kafka_cluster.demo.bootstrap_endpoint, "SASL_SSL://", "") },
    { name = "APIDIAG_SCHEMA_REGISTRY_URL", value = data.confluent_schema_registry_cluster.demo.rest_endpoint },
    { name = "APIDIAG_TOPIC", value = local.topic },
    { name = "APIDIAG_AI_REPORTS", value = tostring(var.ai_agent_deployed) },
  ]
  app_secrets = [
    { name = "APIDIAG_ACCESS_TOKEN", valueFrom = local.from_secret["ACCESS_TOKEN"] },
    { name = "APIDIAG_KAFKA_API_KEY", valueFrom = local.from_secret["KAFKA_API_KEY"] },
    { name = "APIDIAG_KAFKA_API_SECRET", valueFrom = local.from_secret["KAFKA_API_SECRET"] },
    { name = "APIDIAG_SCHEMA_REGISTRY_API_KEY", valueFrom = local.from_secret["SCHEMA_REGISTRY_KEY"] },
    { name = "APIDIAG_SCHEMA_REGISTRY_API_SECRET", valueFrom = local.from_secret["SCHEMA_REGISTRY_SECRET"] },
  ]
}

resource "aws_cloudwatch_log_group" "demo" {
  name              = "/${var.launchpad_demo_id}/${local.name}"
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

# The execution role is ECS's own: it pulls the image and injects the secrets. The app
# itself has no AWS permissions and needs none.
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

resource "aws_ecs_cluster" "demo" {
  name = local.name
}

resource "aws_ecs_task_definition" "demo" {
  family                   = local.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.task_cpu
  memory                   = var.task_memory
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([
    merge({
      name         = "app"
      image        = "${var.image_registry}/automated-api-diagnosis:${var.image_tag}"
      essential    = true
      portMappings = [{ containerPort = 8080, protocol = "tcp" }]
      environment  = local.app_env
      secrets      = local.app_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.demo.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "app"
        }
      }
    }, local.registry_auth),
  ])
}

resource "aws_ecs_service" "demo" {
  name            = local.name
  cluster         = aws_ecs_cluster.demo.id
  task_definition = aws_ecs_task_definition.demo.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  # Never two tasks: two simulators would send twice the traffic to one topic.
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
  health_check_grace_period_seconds  = 60

  # A task that cannot start, for example because the image is private, fails the
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
    container_name   = "app"
    container_port   = 8080
  }

  wait_for_steady_state = true

  depends_on = [
    aws_lb_listener.http,
    aws_secretsmanager_secret_version.demo,
    aws_iam_role_policy.execution_secrets,
    # The monitor reads these two topics from the moment it starts, and Flink creates them.
    confluent_flink_statement.create_unhealthy_events,
    confluent_flink_statement.create_anomaly_reports,
  ]
}
