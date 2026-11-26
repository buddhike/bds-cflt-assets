# Launchpad's interface: where the user goes to use the demo. The link carries the
# access token, so the output is sensitive: Terraform will not print it, and
# `terraform output -raw launchpad_url` still reads it.
output "launchpad_url" {
  description = "Open this to use the demo. It includes the access token."
  value       = "https://${aws_cloudfront_distribution.demo.domain_name}/?access=${random_password.access_token.result}"
  sensitive   = true
}

output "cloudfront_domain_name" {
  value = aws_cloudfront_distribution.demo.domain_name
}

output "environment_id" {
  value = confluent_environment.demo.id
}

output "kafka_cluster_id" {
  value = confluent_kafka_cluster.demo.id
}

output "flink_compute_pool_id" {
  description = "Where the demo's Flink statements run."
  value       = confluent_flink_compute_pool.demo.id
}

output "log_group" {
  description = "CloudWatch log group with the app's output."
  value       = aws_cloudwatch_log_group.demo.name
}
