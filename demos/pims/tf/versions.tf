# PIMS as a Launchpad demo: Confluent Cloud for the data, ECS for the app.
# See docs/specs/launchpad.md for how the parts fit.

terraform {
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    confluent = {
      source  = "confluentinc/confluent"
      version = "~> 2.90"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

provider "aws" {
  region = var.aws_region

  # Launchpad's tags go on everything that takes tags.
  default_tags {
    tags = var.launchpad_tags
  }
}

provider "confluent" {
  cloud_api_key    = var.confluent_cloud_api_key
  cloud_api_secret = var.confluent_cloud_api_secret
}
