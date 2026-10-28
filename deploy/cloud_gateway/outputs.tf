output "gateway_external_ip" {
  value       = google_compute_address.gateway_ip.address
  description = "Reserved static external IPv4 for ztd Gateway"
}

output "instance_name" {
  value       = google_compute_instance.gateway_vm.name
  description = "Compute Engine instance name"
}

output "zone" {
  value       = var.zone
  description = "Compute Engine zone"
}

output "ssh_command" {
  value       = "gcloud compute ssh ${google_compute_instance.gateway_vm.name} --zone=${var.zone} --project=${var.project_id}"
  description = "GCloud SSH command to access the Gateway instance"
}

