terraform {
  required_providers {
    linode = {
      source  = "linode/linode"
      version = "~> 2.9"
    }
  }
}

variable "cluster_label" {
  description = "Label for the LKE cluster"
  type        = string
}

variable "region" {
  description = "Linode region for the cluster"
  type        = string
}

variable "k8s_version" {
  description = "Kubernetes version"
  type        = string
}

variable "tags" {
  description = "Tags to apply to the cluster"
  type        = list(string)
  default     = []
}

variable "node_pools" {
  description = "Standard node pool configurations"
  type = list(object({
    type   = string
    count  = number
    labels = optional(map(string), {})
  }))
}

variable "gpu_node_pools" {
  description = "GPU node pool configurations"
  type = list(object({
    type   = string
    count  = number
    labels = optional(map(string), {})
    taints = optional(list(object({
      key    = string
      value  = string
      effect = string
    })), [])
  }))
  default = []
}

resource "linode_lke_cluster" "cluster" {
  label       = var.cluster_label
  k8s_version = var.k8s_version
  region      = var.region
  tags        = var.tags

  # Standard node pools
  dynamic "pool" {
    for_each = var.node_pools
    content {
      type  = pool.value.type
      count = pool.value.count

      dynamic "autoscaler" {
        for_each = [] # Disabled for demo simplicity
        content {
          min = autoscaler.value.min
          max = autoscaler.value.max
        }
      }
    }
  }

  # GPU node pools
  dynamic "pool" {
    for_each = var.gpu_node_pools
    content {
      type  = pool.value.type
      count = pool.value.count
    }
  }
}

output "cluster_id" {
  value = linode_lke_cluster.cluster.id
}

output "api_endpoint" {
  value = linode_lke_cluster.cluster.api_endpoints[0]
}

output "kubeconfig" {
  value     = linode_lke_cluster.cluster.kubeconfig
  sensitive = true
}

output "pool_ids" {
  value = linode_lke_cluster.cluster.pool[*].id
}
