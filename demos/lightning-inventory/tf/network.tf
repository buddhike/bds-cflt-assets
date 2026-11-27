# A small VPC of its own, so tearing the demo down leaves nothing behind. The tasks have a
# public address and no NAT gateway (which would cost more than the demo): only the load
# balancer can reach the control task, and the producer and query tasks only
# call out. The load balancer is internal: CloudFront, which speaks HTTPS to people, reaches
# it, and so do the worker tasks, to poll the control service.

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)
}

resource "aws_vpc" "demo" {
  cidr_block           = "10.42.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = local.name }
}

resource "aws_internet_gateway" "demo" {
  vpc_id = aws_vpc.demo.id
  tags   = { Name = local.name }
}

resource "aws_subnet" "public" {
  count = length(local.azs)

  vpc_id                  = aws_vpc.demo.id
  availability_zone       = local.azs[count.index]
  cidr_block              = cidrsubnet(aws_vpc.demo.cidr_block, 8, count.index)
  map_public_ip_on_launch = true

  tags = { Name = "${local.name}-public-${local.azs[count.index]}" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.demo.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.demo.id
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
  description = "Lightning inventory load balancer"
  vpc_id      = aws_vpc.demo.id
}

resource "aws_security_group" "control" {
  name        = "${local.name}-control"
  description = "Lightning inventory control task"
  vpc_id      = aws_vpc.demo.id
}

# The producer and query tasks. Nothing connects to them.
resource "aws_security_group" "worker" {
  name        = "${local.name}-worker"
  description = "Lightning inventory producer and query tasks"
  vpc_id      = aws_vpc.demo.id
}

# The load balancer is internal. Its only client is CloudFront, through a VPC origin
# (see cloudfront.tf), whose network interfaces carry this security group.
resource "aws_vpc_security_group_ingress_rule" "alb_from_cloudfront" {
  security_group_id            = aws_security_group.alb.id
  description                  = "HTTP from the CloudFront VPC origin"
  ip_protocol                  = "tcp"
  from_port                    = 80
  to_port                      = 80
  referenced_security_group_id = data.aws_security_group.cloudfront_vpc_origin.id
}

# The workers poll the control service through the load balancer (plain HTTP inside the VPC,
# as CloudFront's hop is). The app admits /internal/ only with the worker token and never
# when the request came through CloudFront.
resource "aws_vpc_security_group_ingress_rule" "alb_from_workers" {
  security_group_id            = aws_security_group.alb.id
  description                  = "HTTP from the producer and query tasks"
  ip_protocol                  = "tcp"
  from_port                    = 80
  to_port                      = 80
  referenced_security_group_id = aws_security_group.worker.id
}

resource "aws_vpc_security_group_egress_rule" "alb_to_control" {
  security_group_id            = aws_security_group.alb.id
  description                  = "To the control task"
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  referenced_security_group_id = aws_security_group.control.id
}

# Only the load balancer reaches the app.
resource "aws_vpc_security_group_ingress_rule" "control_from_alb" {
  security_group_id            = aws_security_group.control.id
  description                  = "App from the load balancer"
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  referenced_security_group_id = aws_security_group.alb.id
}

# The control task calls Confluent Cloud, CloudWatch and the image registry.
resource "aws_vpc_security_group_egress_rule" "control_out" {
  security_group_id = aws_security_group.control.id
  description       = "Out"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# The workers call Confluent Cloud (Kafka and the query API), CloudWatch, the control
# service and the image registry.
resource "aws_vpc_security_group_egress_rule" "worker_out" {
  security_group_id = aws_security_group.worker.id
  description       = "Out"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_lb" "demo" {
  name               = local.name
  load_balancer_type = "application"
  # No address on the internet: CloudFront reaches it from inside the VPC.
  internal        = true
  subnets         = aws_subnet.public[*].id
  security_groups = [aws_security_group.alb.id]
  # No request takes long (the slowest is a chart read, 10 s); CloudFront waits 60.
  idle_timeout = 60
}

resource "aws_lb_target_group" "app" {
  name                 = local.name
  vpc_id               = aws_vpc.demo.id
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
  load_balancer_arn = aws_lb.demo.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.app.arn
  }
}
