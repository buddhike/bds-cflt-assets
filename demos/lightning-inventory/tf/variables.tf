# The first five are Launchpad's interface. See demos/launchpad/README.md.

variable "aws_region" {
  description = "Region for the ECS services and the Confluent Cloud cluster. Set by Launchpad. It must be a region where Confluent Cloud offers Lightning Tables on AWS (docs.confluent.io/cloud/current/clusters/regions.html, the Lightning Tables row)."
  type        = string

  validation {
    condition = contains([
      "us-east-1", "us-east-2", "us-west-2",
      "ap-northeast-1", "ap-northeast-2", "ap-southeast-2", "ap-south-1", "ap-southeast-1",
      "eu-central-1", "eu-west-1", "eu-west-2",
    ], var.aws_region)
    error_message = "Lightning Tables are available only in us-east-1, us-east-2, us-west-2, ap-northeast-1, ap-northeast-2, ap-southeast-2, ap-south-1, ap-southeast-1, eu-central-1, eu-west-1 and eu-west-2 (Confluent Cloud region availability)."
  }
}

variable "launchpad_tags" {
  description = "Tags for every resource that takes them. Set by Launchpad."
  type        = map(string)
}

variable "launchpad_demo_id" {
  description = "The demo's directory name. Set by Launchpad."
  type        = string
  default     = "lightning-inventory"
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
  description = "Where the lightning-inventory image is, without a trailing slash. Published by .github/workflows/lightning-inventory-image.yml."
  type        = string
  default     = "ghcr.io/buddhike"
}

variable "image_tag" {
  description = "Tag of the image. Leave it null to deploy the commit Launchpad downloaded the demo at (launchpad_ref), or latest outside Launchpad."
  type        = string
  default     = null
}

variable "launchpad_ref" {
  description = "The commit SHA Launchpad downloaded the demo at. Set by Launchpad. It is the image tag, so an update starts the images of its own commit: a tag that never changes (latest) leaves the task definitions as they were."
  type        = string
  default     = ""
}

variable "registry_credentials_secret_arn" {
  description = "ARN of a Secrets Manager secret with the username and password for the image registry, if its package is private. Leave null for a public package."
  type        = string
  default     = null
}

variable "partitions" {
  description = "Partitions of the inventory_positions topic. Do not change on a running demo: it replaces the topic and loses its data."
  type        = number
  default     = 6
}

variable "locations_per_sku" {
  description = "Mean number of locations that stock a SKU in the catalogue (1 to 100): 10 makes about 10 million positions. The producers and the query tasks must agree, so both get this value."
  type        = number
  default     = 10

  validation {
    condition     = var.locations_per_sku >= 1 && var.locations_per_sku <= 100 && floor(var.locations_per_sku) == var.locations_per_sku
    error_message = "locations_per_sku must be a whole number from 1 to 100."
  }
}

variable "miss_ratio" {
  description = "Share of queries (0 to 0.2) that ask for a SKU that does not exist, to measure misses."
  type        = number
  default     = 0.02

  validation {
    condition     = var.miss_ratio >= 0 && var.miss_ratio <= 0.2
    error_message = "miss_ratio must be from 0 to 0.2."
  }
}

variable "control_cpu" {
  description = "Fargate CPU units of the control task (UI, API, table)."
  type        = number
  default     = 512
}

variable "control_memory" {
  description = "Fargate memory in MiB of the control task."
  type        = number
  default     = 1024
}

variable "producer_cpu" {
  description = "Fargate CPU units of a producer task."
  type        = number
  default     = 512
}

variable "producer_memory" {
  description = "Fargate memory in MiB of a producer task."
  type        = number
  default     = 1024
}

variable "query_cpu" {
  description = "Fargate CPU units of a query task. 1000 queries per second with answers of up to 2000 rows is mostly TLS and JSON, so it gets a whole vCPU."
  type        = number
  default     = 1024
}

variable "query_memory" {
  description = "Fargate memory in MiB of a query task (512 requests in flight, each holding up to a few hundred KB)."
  type        = number
  default     = 2048
}

variable "producer_min_tasks" {
  description = "Fewest producer tasks. The service scales out on CPU, and the rate is split between the tasks."
  type        = number
  default     = 1
}

variable "producer_max_tasks" {
  description = "Most producer tasks service auto scaling may run."
  type        = number
  default     = 4
}

variable "query_min_tasks" {
  description = "Fewest query tasks. The service scales out on CPU, and the rate is split between the tasks."
  type        = number
  default     = 1
}

variable "query_max_tasks" {
  description = "Most query tasks service auto scaling may run."
  type        = number
  default     = 8
}
