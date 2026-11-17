variable "aws_region" {
  description = "AWS region for the Kafka cluster and Tableflow storage, e.g. us-east-1."
  type        = string
}

variable "name" {
  description = "Display name prefix for every Confluent Cloud resource."
  type        = string
  default     = "pims"
}

variable "partitions" {
  description = "Partitions per CDC topic."
  type        = number
  default     = 1
}

variable "enable_tableflow" {
  description = "Enable Tableflow on the CDC topics. Requires the topic schemas to exist."
  type        = bool
  default     = false
}
