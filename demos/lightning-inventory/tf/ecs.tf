# Three ECS services run the one image, by LIGHTNING_ROLE:
#
#   control   the UI and the API; it reads the Lightning Table's status and the usage metrics
#             and changes nothing. One task, behind the load balancer on port 8080.
#   producer  the live events and the one-off load of the catalogue. Scales out on CPU.
#   query     the query load, and the latency metrics to CloudWatch. Scales out on CPU.
#
# The workers poll the control service for the rate; the operator's total is split between
# however many tasks are running, so scaling never changes the load.

data "aws_partition" "current" {}

locals {
  image_tag  = coalesce(var.image_tag, var.launchpad_ref, "latest")
  image      = "${var.image_registry}/lightning-inventory:${local.image_tag}"
  secret_arn = aws_secretsmanager_secret.demo.arn

  # The custom CloudWatch namespace the query tasks publish to and the control task reads.
  # One per deployment, so the IAM condition below can name exactly it.
  metrics_namespace = "LightningInventory/${local.name}"

  # A secret's JSON key as a container secret: <arn>:<key>::
  from_secret = { for k in [
    "ACCESS_TOKEN", "WORKER_TOKEN", "KAFKA_API_KEY", "KAFKA_API_SECRET",
    "GLOBAL_API_KEY", "GLOBAL_API_SECRET", "STATUS_API_KEY", "STATUS_API_SECRET",
    "CLOUD_API_KEY", "CLOUD_API_SECRET",
  ] : k => "${local.secret_arn}:${k}::" }

  # A private package needs the registry's credentials.
  registry_auth = var.registry_credentials_secret_arn == null ? {} : {
    repositoryCredentials = { credentialsParameter = var.registry_credentials_secret_arn }
  }

  # The contract in docs/specs/launchpad.md, per role. contract_test.go checks each list
  # against what the Go config requires and reads for that role.
  control_env = [
    { name = "LIGHTNING_ROLE", value = "control" },
    { name = "LIGHTNING_AWS_REGION", value = var.aws_region },
    { name = "LIGHTNING_DEMO_NAME", value = local.name },
    { name = "LIGHTNING_ENVIRONMENT_ID", value = confluent_environment.demo.id },
    { name = "LIGHTNING_KAFKA_CLUSTER_ID", value = confluent_kafka_cluster.demo.id },
    { name = "LIGHTNING_TOPIC", value = local.topic },
    { name = "LIGHTNING_METRICS_NAMESPACE", value = local.metrics_namespace },
  ]
  control_secrets = [
    { name = "LIGHTNING_ACCESS_TOKEN", valueFrom = local.from_secret["ACCESS_TOKEN"] },
    { name = "LIGHTNING_WORKER_TOKEN", valueFrom = local.from_secret["WORKER_TOKEN"] },
    { name = "LIGHTNING_STATUS_API_KEY", valueFrom = local.from_secret["STATUS_API_KEY"] },
    { name = "LIGHTNING_STATUS_API_SECRET", valueFrom = local.from_secret["STATUS_API_SECRET"] },
    { name = "LIGHTNING_CLOUD_API_KEY", valueFrom = local.from_secret["CLOUD_API_KEY"] },
    { name = "LIGHTNING_CLOUD_API_SECRET", valueFrom = local.from_secret["CLOUD_API_SECRET"] },
  ]

  producer_env = [
    { name = "LIGHTNING_ROLE", value = "producer" },
    { name = "LIGHTNING_AWS_REGION", value = var.aws_region },
    { name = "LIGHTNING_DEMO_NAME", value = local.name },
    { name = "LIGHTNING_TOPIC", value = local.topic },
    { name = "LIGHTNING_KAFKA_BOOTSTRAP", value = replace(confluent_kafka_cluster.demo.bootstrap_endpoint, "SASL_SSL://", "") },
    { name = "LIGHTNING_VALUE_SCHEMA_ID", value = tostring(confluent_schema.inventory_value.schema_identifier) },
    { name = "LIGHTNING_LOCATIONS_PER_SKU", value = tostring(var.locations_per_sku) },
    { name = "LIGHTNING_CONTROL_URL", value = "http://${aws_lb.demo.dns_name}" },
  ]
  producer_secrets = [
    { name = "LIGHTNING_WORKER_TOKEN", valueFrom = local.from_secret["WORKER_TOKEN"] },
    { name = "LIGHTNING_KAFKA_API_KEY", valueFrom = local.from_secret["KAFKA_API_KEY"] },
    { name = "LIGHTNING_KAFKA_API_SECRET", valueFrom = local.from_secret["KAFKA_API_SECRET"] },
  ]

  query_env = [
    { name = "LIGHTNING_ROLE", value = "query" },
    { name = "LIGHTNING_AWS_REGION", value = var.aws_region },
    { name = "LIGHTNING_DEMO_NAME", value = local.name },
    { name = "LIGHTNING_ENVIRONMENT_ID", value = confluent_environment.demo.id },
    { name = "LIGHTNING_KAFKA_CLUSTER_ID", value = confluent_kafka_cluster.demo.id },
    { name = "LIGHTNING_TOPIC", value = local.topic },
    { name = "LIGHTNING_LOCATIONS_PER_SKU", value = tostring(var.locations_per_sku) },
    { name = "LIGHTNING_MISS_RATIO", value = tostring(var.miss_ratio) },
    { name = "LIGHTNING_METRICS_NAMESPACE", value = local.metrics_namespace },
    { name = "LIGHTNING_CONTROL_URL", value = "http://${aws_lb.demo.dns_name}" },
  ]
  query_secrets = [
    { name = "LIGHTNING_WORKER_TOKEN", valueFrom = local.from_secret["WORKER_TOKEN"] },
    { name = "LIGHTNING_GLOBAL_API_KEY", valueFrom = local.from_secret["GLOBAL_API_KEY"] },
    { name = "LIGHTNING_GLOBAL_API_SECRET", valueFrom = local.from_secret["GLOBAL_API_SECRET"] },
  ]

  roles = {
    control = {
      env           = local.control_env, secrets = local.control_secrets, cpu = var.control_cpu, memory = var.control_memory
      task_role_arn = aws_iam_role.control.arn
    }
    producer = {
      env           = local.producer_env, secrets = local.producer_secrets, cpu = var.producer_cpu, memory = var.producer_memory
      task_role_arn = null # the producers call Kafka only: no AWS permissions at all
    }
    query = {
      env           = local.query_env, secrets = local.query_secrets, cpu = var.query_cpu, memory = var.query_memory
      task_role_arn = aws_iam_role.query.arn
    }
  }
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

# The execution role is ECS's own: it pulls the image and injects the secrets.
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

# The app gets AWS permissions only where it truly needs them, and exactly these (the
# exception to "the app has no AWS permissions" is recorded in docs/specs/launchpad.md):
#
#   query    PutMetricData, only into this deployment's namespace. PutMetricData has no
#            resource-level scoping; the namespace condition is the restriction.
#   control  GetMetricData, to draw the charts. It supports no resource or namespace
#            condition, so it is "*". It can read metrics, nothing else.
#   producer none.
resource "aws_iam_role" "query" {
  name               = "${local.name}-query"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

resource "aws_iam_role_policy" "query" {
  name = "publish-latency"
  role = aws_iam_role.query.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = ["cloudwatch:PutMetricData"]
      Resource  = "*"
      Condition = { StringEquals = { "cloudwatch:namespace" = local.metrics_namespace } }
    }]
  })
}

resource "aws_iam_role" "control" {
  name               = "${local.name}-control"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

resource "aws_iam_role_policy" "control" {
  name = "read-latency"
  role = aws_iam_role.control.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["cloudwatch:GetMetricData"]
      Resource = "*"
    }]
  })
}

resource "aws_ecs_cluster" "demo" {
  name = local.name
}

resource "aws_ecs_task_definition" "role" {
  for_each = local.roles

  family                   = "${local.name}-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = each.value.cpu
  memory                   = each.value.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = each.value.task_role_arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([
    merge({
      name         = "app"
      image        = local.image
      essential    = true
      portMappings = each.key == "control" ? [{ containerPort = 8080, protocol = "tcp" }] : []
      environment  = each.value.env
      secrets      = each.value.secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.demo.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = each.key
        }
      }
    }, local.registry_auth),
  ])
}

# One control task, never two: it holds the rates.
resource "aws_ecs_service" "control" {
  name            = "${local.name}-control"
  cluster         = aws_ecs_cluster.demo.id
  task_definition = aws_ecs_task_definition.role["control"].arn
  desired_count   = 1
  launch_type     = "FARGATE"

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
    security_groups  = [aws_security_group.control.id]
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
    aws_iam_role_policy.control,
    time_sleep.wait_for_app_keys,
  ]
}

locals {
  workers = {
    producer = { min = var.producer_min_tasks, max = var.producer_max_tasks }
    query    = { min = var.query_min_tasks, max = var.query_max_tasks }
  }
}

# The producer and query tasks. They start (and stay) idle until the operator clicks Start,
# so the service's desired count is only about capacity. Auto scaling owns it after the first
# apply.
resource "aws_ecs_service" "worker" {
  for_each = local.workers

  name            = "${local.name}-${each.key}"
  cluster         = aws_ecs_cluster.demo.id
  task_definition = aws_ecs_task_definition.role[each.key].arn
  desired_count   = each.value.min
  launch_type     = "FARGATE"

  # Replaced tasks overlap with the old ones. Control splits the rate by the tasks it hears
  # from, so the total stays what the operator set.
  deployment_minimum_healthy_percent = 50
  deployment_maximum_percent         = 200

  deployment_circuit_breaker {
    enable   = true
    rollback = false
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.worker.id]
    assign_public_ip = true
  }

  wait_for_steady_state = true

  # The table first: on destroy the workers stop before the table and the topic go.
  depends_on = [
    aws_ecs_service.control,
    aws_iam_role_policy.execution_secrets,
    aws_iam_role_policy.query,
    time_sleep.wait_for_app_keys,
    confluent_rtce_topic.inventory,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_appautoscaling_target" "worker" {
  for_each = local.workers

  service_namespace  = "ecs"
  scalable_dimension = "ecs:service:DesiredCount"
  resource_id        = "service/${aws_ecs_cluster.demo.name}/${aws_ecs_service.worker[each.key].name}"
  min_capacity       = each.value.min
  max_capacity       = each.value.max
}

# Scale out when the tasks are busy, in a minute; in again after five quiet ones.
resource "aws_appautoscaling_policy" "worker_cpu" {
  for_each = local.workers

  name               = "${local.name}-${each.key}-cpu"
  policy_type        = "TargetTrackingScaling"
  service_namespace  = aws_appautoscaling_target.worker[each.key].service_namespace
  scalable_dimension = aws_appautoscaling_target.worker[each.key].scalable_dimension
  resource_id        = aws_appautoscaling_target.worker[each.key].resource_id

  target_tracking_scaling_policy_configuration {
    target_value       = 60
    scale_out_cooldown = 60
    scale_in_cooldown  = 300

    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}
