terraform {
  required_version = ">= 1.5.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone
}

# Static External IPv4 Address: Prevents TLS SAN certificate drift and DNS breakage
resource "google_compute_address" "gateway_ip" {
  name        = "${var.instance_name}-static-ip"
  region      = var.region
  description = "Static external IPv4 for ztd Gateway PDP and Operator mTLS transport"
}

# Ingress Firewall Rule: Permits SSH (22), HTTP Discovery (8080), and HTTPS mTLS (8443)
resource "google_compute_firewall" "allow_gateway" {
  name        = "allow-${var.instance_name}"
  network     = "default"
  description = "Permit SSH, HTTP bootstrap, and HTTPS mTLS for ztd Gateway per INV-DEPLOY-NET-01"

  allow {
    protocol = "tcp"
    ports    = ["22", "8080", "8443"]
  }

  allow {
    protocol = "icmp"
  }

  source_ranges = ["0.0.0.0/0"]
  target_tags   = [var.instance_name]
}

# Compute Engine Instance (Debian 12)
resource "google_compute_instance" "gateway_vm" {
  name         = var.instance_name
  machine_type = var.machine_type
  zone         = var.zone
  tags         = [var.instance_name]

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = var.boot_disk_size_gb
      type  = "pd-standard"
    }
  }

  network_interface {
    network = "default"
    access_config {
      nat_ip = google_compute_address.gateway_ip.address
    }
  }

  metadata = {
    enable-oslogin = "TRUE"
  }

  metadata_startup_script = file("${path.module}/startup.sh")
}

