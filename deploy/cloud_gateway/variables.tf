variable "project_id" {
  description = "Google Cloud Platform Project ID"
  type        = string
  default     = "zerotrustdata"
}

variable "region" {
  description = "Google Cloud Platform Region"
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "Google Cloud Platform Zone"
  type        = string
  default     = "us-central1-a"
}

variable "instance_name" {
  description = "Compute Engine instance name"
  type        = string
  default     = "ztd-gateway"
}

variable "machine_type" {
  description = "Compute Engine machine type (min 2 vCPU, 2-4GB RAM per INV-DEPLOY-HOST-03)"
  type        = string
  default     = "e2-medium"
}

variable "boot_disk_size_gb" {
  description = "Boot disk size in GB"
  type        = number
  default     = 20
}

