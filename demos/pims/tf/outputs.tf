# Launchpad's interface: where the user goes to use the demo. The link carries the
# access token, so the output is sensitive: Terraform will not print it, and
# `terraform output -raw launchpad_url` still reads it.
output "launchpad_url" {
  description = "Open this to use PIMS. It includes the access token."
  value       = "http://${aws_lb.pims.dns_name}/?access=${random_password.access_token.result}"
  sensitive   = true
}

output "load_balancer_dns_name" {
  value = aws_lb.pims.dns_name
}

output "environment_id" {
  value = confluent_environment.pims.id
}

output "kafka_cluster_id" {
  value = confluent_kafka_cluster.pims.id
}

output "log_group" {
  description = "CloudWatch log group with the output of all four containers."
  value       = aws_cloudwatch_log_group.pims.name
}
