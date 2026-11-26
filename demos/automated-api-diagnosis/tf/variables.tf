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
  description = "Tag of the image. Leave it null to deploy the commit Launchpad downloaded the demo at (launchpad_ref), or latest outside Launchpad. The workflow publishes a commit SHA, a branch name and latest."
  type        = string
  default     = null
}

variable "launchpad_ref" {
  description = "The commit SHA Launchpad downloaded the demo at. Set by Launchpad. It is the image tag, so an update starts the images of its own commit: a tag that never changes (latest) leaves the task definition as it was, and ECS keeps the old task."
  type        = string
  default     = ""
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
  description = "Most Confluent Flink Units the compute pool may use. The anomaly detection and AI agent statements need a few each. 5 is the smallest a pool may have."
  type        = number
  default     = 10
}

variable "bedrock_model_id" {
  description = "The Amazon Bedrock model the AI agent uses. The account needs access to it enabled in the Bedrock console."
  type        = string
  default     = "openai.gpt-oss-120b-1:0"
}

variable "bedrock_region" {
  description = "Region of the Bedrock model. It need not be the demo's own region, but the model must be offered there."
  type        = string
  default     = "ap-southeast-2"
}

variable "udf_release_repository" {
  description = "The GitHub repository (owner/name) whose releases hold the EmitAfterGap jar. The automated-api-diagnosis-udf workflow publishes it there."
  type        = string
  default     = "buddhike/bds-cflt-assets"
}
