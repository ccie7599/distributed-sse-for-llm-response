variable "linode_token" {
  description = "Linode API token"
  type        = string
  sensitive   = true
}

variable "k8s_version" {
  description = "Kubernetes version for LKE clusters"
  type        = string
  default     = "1.29"
}

variable "environment" {
  description = "Environment name (dev, staging, prod)"
  type        = string
  default     = "demo"
}

variable "nats_leaf_password" {
  description = "Password for NATS leaf node authentication"
  type        = string
  sensitive   = true
  default     = ""  # Will be auto-generated if not provided
}
