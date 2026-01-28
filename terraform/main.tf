terraform {
  required_version = ">= 1.3.0"

  required_providers {
    linode = {
      source  = "linode/linode"
      version = "~> 2.9"
    }
  }
}

provider "linode" {
  token = var.linode_token
}

locals {
  regions = {
    "us-ord" = {
      label       = "origin"
      is_origin   = true
      has_gpu     = true
    }
    "us-lax" = {
      label       = "edge-lax"
      is_origin   = false
      has_gpu     = false
    }
    "us-mia" = {
      label       = "edge-mia"
      is_origin   = false
      has_gpu     = false
    }
  }

  common_tags = ["distributed-sse", "demo"]
}

# Origin cluster (us-ord) - includes GPU nodes for LLM
module "origin_cluster" {
  source = "./modules/lke-cluster"

  cluster_label = "sse-origin"
  region        = "us-ord"
  k8s_version   = var.k8s_version
  tags          = local.common_tags

  # Standard node pool for NATS, Redis, etc.
  node_pools = [
    {
      type  = "g6-standard-4"  # 4 CPU, 8GB RAM
      count = 3
      labels = {
        "node-role" = "infra"
      }
    }
  ]

  # GPU node pool for LLM inference - RTX 4000 Ada (supported by LKE)
  gpu_node_pools = [
    {
      type  = "g2-gpu-rtx4000a1-m"  # RTX 4000 Ada, 8 vCPUs, 32GB RAM, 20GB VRAM
      count = 1
      labels = {
        "node-role"      = "gpu"
        "nvidia.com/gpu" = "true"
      }
      taints = [
        {
          key    = "nvidia.com/gpu"
          value  = "true"
          effect = "NoSchedule"
        }
      ]
    }
  ]
}

# Edge clusters (us-lax, us-mia) - no GPU, just NATS leaf + SSE adapter
module "edge_cluster_lax" {
  source = "./modules/lke-cluster"

  cluster_label = "sse-edge-lax"
  region        = "us-lax"
  k8s_version   = var.k8s_version
  tags          = local.common_tags

  node_pools = [
    {
      type  = "g6-standard-2"  # 2 CPU, 4GB RAM
      count = 3
      labels = {
        "node-role" = "edge"
      }
    }
  ]

  gpu_node_pools = []
}

module "edge_cluster_mia" {
  source = "./modules/lke-cluster"

  cluster_label = "sse-edge-mia"
  region        = "us-mia"
  k8s_version   = var.k8s_version
  tags          = local.common_tags

  node_pools = [
    {
      type  = "g6-standard-2"
      count = 3
      labels = {
        "node-role" = "edge"
      }
    }
  ]

  gpu_node_pools = []
}

# Output kubeconfig files
resource "local_file" "origin_kubeconfig" {
  content         = base64decode(module.origin_cluster.kubeconfig)
  filename        = "${path.module}/../kubeconfigs/origin.yaml"
  file_permission = "0600"
}

resource "local_file" "edge_lax_kubeconfig" {
  content         = base64decode(module.edge_cluster_lax.kubeconfig)
  filename        = "${path.module}/../kubeconfigs/edge-lax.yaml"
  file_permission = "0600"
}

resource "local_file" "edge_mia_kubeconfig" {
  content         = base64decode(module.edge_cluster_mia.kubeconfig)
  filename        = "${path.module}/../kubeconfigs/edge-mia.yaml"
  file_permission = "0600"
}

# Outputs for use in deployment scripts
output "origin_cluster_id" {
  value = module.origin_cluster.cluster_id
}

output "origin_api_endpoint" {
  value = module.origin_cluster.api_endpoint
}

output "edge_lax_cluster_id" {
  value = module.edge_cluster_lax.cluster_id
}

output "edge_lax_api_endpoint" {
  value = module.edge_cluster_lax.api_endpoint
}

output "edge_mia_cluster_id" {
  value = module.edge_cluster_mia.cluster_id
}

output "edge_mia_api_endpoint" {
  value = module.edge_cluster_mia.api_endpoint
}

output "nats_core_endpoint" {
  description = "NATS core cluster endpoint for leaf node connections"
  value       = "nats-core.${module.origin_cluster.api_endpoint}:7422"
}
