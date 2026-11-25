# A small VPC of its own, so tearing the demo down leaves nothing behind. The task
# has a public address and no NAT gateway (which would cost more than the demo):
# only the load balancer can reach it, and the task only calls out.

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)
}

resource "aws_vpc" "pims" {
  cidr_block           = "10.42.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = local.name }
}

resource "aws_internet_gateway" "pims" {
  vpc_id = aws_vpc.pims.id
  tags   = { Name = local.name }
}

resource "aws_subnet" "public" {
  count = length(local.azs)

  vpc_id                  = aws_vpc.pims.id
  availability_zone       = local.azs[count.index]
  cidr_block              = cidrsubnet(aws_vpc.pims.cidr_block, 8, count.index)
  map_public_ip_on_launch = true

  tags = { Name = "${local.name}-public-${local.azs[count.index]}" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.pims.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.pims.id
  }

  tags = { Name = "${local.name}-public" }
}

resource "aws_route_table_association" "public" {
  count = length(aws_subnet.public)

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

resource "aws_security_group" "alb" {
  name        = "${local.name}-alb"
  description = "PIMS load balancer"
  vpc_id      = aws_vpc.pims.id
}

resource "aws_security_group" "task" {
  name        = "${local.name}-task"
  description = "PIMS ECS task"
  vpc_id      = aws_vpc.pims.id
}

resource "aws_vpc_security_group_ingress_rule" "alb_http" {
  for_each = toset(var.allowed_cidr_blocks)

  security_group_id = aws_security_group.alb.id
  description       = "HTTP from ${each.key}"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv4         = each.key
}

resource "aws_vpc_security_group_egress_rule" "alb_to_task" {
  security_group_id            = aws_security_group.alb.id
  description                  = "To the task"
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  referenced_security_group_id = aws_security_group.task.id
}

# Only the load balancer reaches the app. MySQL, Connect and the DuckDB agent
# listen inside the task and have no rule at all.
resource "aws_vpc_security_group_ingress_rule" "task_from_alb" {
  security_group_id            = aws_security_group.task.id
  description                  = "App from the load balancer"
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  referenced_security_group_id = aws_security_group.alb.id
}

# The task calls Confluent Cloud, the Metrics API, SSM and the image registries.
resource "aws_vpc_security_group_egress_rule" "task_out" {
  security_group_id = aws_security_group.task.id
  description       = "Out"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_lb" "pims" {
  name               = local.name
  load_balancer_type = "application"
  internal           = false
  subnets            = aws_subnet.public[*].id
  security_groups    = [aws_security_group.alb.id]
  # The UI holds a server-sent event stream open.
  idle_timeout = 120
}

resource "aws_lb_target_group" "app" {
  name                 = local.name
  vpc_id               = aws_vpc.pims.id
  target_type          = "ip"
  protocol             = "HTTP"
  port                 = 8080
  deregistration_delay = 10

  health_check {
    path                = "/healthz"
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.pims.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.app.arn
  }
}
