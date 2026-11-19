variable "aws_region" {
  description = "AWS region for the Kafka cluster and Tableflow storage, e.g. us-east-1."
  type        = string
}

variable "name" {
  description = "Display name prefix for every Confluent Cloud resource."
  type        = string
  default     = "pims"
}

variable "name_suffix" {
  description = "Random suffix that makes resource names unique, so operators sharing a Confluent Cloud account can each run the demo. PIMS generates it once and keeps it in settings.json."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]{3,12}$", var.name_suffix))
    error_message = "name_suffix must be 3 to 12 lowercase letters or digits."
  }
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
