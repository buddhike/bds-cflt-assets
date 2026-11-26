# The first five are Launchpad's interface. See demos/launchpad/README.md.

variable "aws_region" {
  description = "Region for the ECS service and the Confluent Cloud cluster. Set by Launchpad."
  type        = string
}

variable "launchpad_tags" {
  description = "Tags for every resource that takes them. Set by Launchpad."
  type        = map(string)
}

variable "launchpad_demo_id" {
  description = "The demo's directory name. Set by Launchpad."
  type        = string
  default     = "pims"
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
  description = "Where the pims-server, pims-duckdb and pims-connect images are, without a trailing slash. Published by .github/workflows/pims-images.yml."
  type        = string
  default     = "ghcr.io/buddhike"
}

variable "image_tag" {
  description = "Tag of the three images. The workflow publishes latest from the default branch, and a branch name and commit SHA from every push."
  type        = string
  default     = "latest"
}

variable "registry_credentials_secret_arn" {
  description = "ARN of a Secrets Manager secret with the username and password for the image registry, if its packages are private. Leave null for public packages."
  type        = string
  default     = null
}

variable "task_cpu" {
  description = "Fargate CPU units for the task that runs all four containers."
  type        = number
  default     = 2048
}

variable "task_memory" {
  description = "Fargate memory in MiB. MySQL, a Kafka Connect JVM, DuckDB (2 GB query limit) and the app share it."
  type        = number
  default     = 8192
}

variable "partitions" {
  description = "Partitions per CDC topic."
  type        = number
  default     = 1
}
