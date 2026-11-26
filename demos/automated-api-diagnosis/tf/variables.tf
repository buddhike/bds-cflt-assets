# The first five are Launchpad's interface. See demos/launchpad/README.md.

variable "aws_region" {
  description = "Region for the ECS service, the Confluent Cloud cluster and its Flink compute pool. Set by Launchpad. It must be a region where Confluent Cloud offers Flink (confluent flink region list --cloud AWS)."
  type        = string
}

variable "launchpad_tags" {
  description = "Tags for every resource that takes them. Set by Launchpad."
  type        = map(string)
}

variable "launchpad_demo_id" {
  description = "The demo's directory name. Set by Launchpad."
  type        = string
  default     = "automated-api-diagnosis"
}

variable "confluent_cloud_api_key" {
  description = "The launching user's Confluent Cloud API key. Set by Launchpad."
  type        = string
  sensitive   = true
}

variable "confluent_cloud_api_secret" {
  description = "The launching user's Confluent Cloud API secret. Set by Launchpad."
  type        = string
  sensitive   = true
}

variable "image_registry" {
  description = "Where the automated-api-diagnosis image is, without a trailing slash. Published by .github/workflows/automated-api-diagnosis-image.yml."
  type        = string
  default     = "ghcr.io/buddhike"
}

variable "image_tag" {
  description = "Tag of the image. The workflow publishes latest from the default branch, and a branch name and commit SHA from every push."
  type        = string
  default     = "latest"
}

variable "registry_credentials_secret_arn" {
  description = "ARN of a Secrets Manager secret with the username and password for the image registry, if its package is private. Leave null for a public package."
  type        = string
  default     = null
}

variable "task_cpu" {
  description = "Fargate CPU units for the task."
  type        = number
  default     = 512
}

variable "task_memory" {
  description = "Fargate memory in MiB."
  type        = number
  default     = 1024
}

variable "partitions" {
  description = "Partitions of the gateway-logs topic."
  type        = number
  default     = 6
}

variable "flink_max_cfu" {
  description = "Most Confluent Flink Units the compute pool may use. The anomaly detection statement needs a few, and an AI agent statement needs more. 5 is the smallest a pool may have."
  type        = number
  default     = 10
}

variable "ai_agent_deployed" {
  description = "Whether ai.tf (copied from ai.tf.example) deploys an AI agent that writes to api_anomaly_ai_reports. It only changes what the AI reports tab says while it is empty."
  type        = bool
  default     = false
}
