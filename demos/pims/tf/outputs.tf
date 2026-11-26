# Launchpad's interface: where the user goes to use the demo. The link carries the
# access token, so the output is sensitive: Terraform will not print it, and
# `terraform output -raw launchpad_url` still reads it.
output "launchpad_url" {
  description = "Open this to use PIMS. It includes the access token."
  value       = "https://${aws_cloudfront_distribution.pims.domain_name}/?access=${random_password.access_token.result}"
  sensitive   = true
}

# Launchpad calls this before it destroys the demo, so the app can switch Tableflow off.
# See docs/specs/launchpad.md. JSON, so the address and the token are read together.
output "launchpad_shutdown" {
  description = "Where and how Launchpad asks PIMS to shut down: a POST to url with the token as a bearer token."
  value = jsonencode({
    url   = "https://${aws_cloudfront_distribution.pims.domain_name}/shutdown"
    token = random_password.access_token.result
  })
  sensitive = true
}

output "cloudfront_domain_name" {
  value = aws_cloudfront_distribution.pims.domain_name
}

output "load_balancer_dns_name" {
  description = "The internal load balancer behind CloudFront. It is not reachable from the internet."
  value       = aws_lb.pims.dns_name
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
