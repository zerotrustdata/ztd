# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Repeatable deployment workflow for ztd Cloud Gateway.

PROJECT_ID ?= zerotrustdata
ZONE ?= us-central1-a
INSTANCE_NAME ?= ztd-gateway

.PHONY: help init plan apply deploy status logs ssh destroy

help:
	@echo "ztd Cloud Gateway Deployment Workflow"
	@echo ""
	@echo "Targets:"
	@echo "  init      Initialize Terraform working directory"
	@echo "  plan      Review infrastructure changes"
	@echo "  apply     Provision or update Cloud VM, static IP, and firewall"
	@echo "  deploy    Build local binary, SCP to VM, install, and restart service"
	@echo "  status    Check systemd daemon status and HTTP health"
	@echo "  logs      Stream systemd logs (journalctl -u ztd-gateway -f)"
	@echo "  ssh       Open SSH session to the Gateway instance"
	@echo "  destroy   Tear down cloud infrastructure"

init:
	terraform init

plan:
	terraform plan -var="project_id=$(PROJECT_ID)" -var="zone=$(ZONE)" -var="instance_name=$(INSTANCE_NAME)"

apply:
	terraform apply -auto-approve -var="project_id=$(PROJECT_ID)" -var="zone=$(ZONE)" -var="instance_name=$(INSTANCE_NAME)"

deploy:
	@echo "[deploy] Building static Linux binary..."
	$(MAKE) -C ../.. build
	@echo "[deploy] Uploading binary to $(INSTANCE_NAME)..."
	gcloud compute scp ../../bin/ztd-linux-amd64 $(INSTANCE_NAME):/tmp/ztd \
		--zone=$(ZONE) \
		--project=$(PROJECT_ID)
	@echo "[deploy] Installing binary and restarting systemd service..."
	gcloud compute ssh $(INSTANCE_NAME) \
		--zone=$(ZONE) \
		--project=$(PROJECT_ID) \
		--command="sudo mv /tmp/ztd /usr/local/bin/ztd && sudo chmod +x /usr/local/bin/ztd && sudo systemctl restart ztd-gateway"
	@echo "[deploy] Verifying health probe..."
	@sleep 2
	@IP=$$(gcloud compute instances describe $(INSTANCE_NAME) --zone=$(ZONE) --project=$(PROJECT_ID) --format='get(networkInterfaces[0].accessConfigs[0].natIP)'); \
	echo "Probing http://$$IP:8080/healthz ..."; \
	curl -sI -m 5 http://$$IP:8080/healthz | head -n 1 || echo "Warning: Direct HTTP probe timed out (check firewall rules if traffic is blocked)."

status:
	gcloud compute ssh $(INSTANCE_NAME) \
		--zone=$(ZONE) \
		--project=$(PROJECT_ID) \
		--command="sudo systemctl status ztd-gateway --no-pager"

logs:
	gcloud compute ssh $(INSTANCE_NAME) \
		--zone=$(ZONE) \
		--project=$(PROJECT_ID) \
		--command="sudo journalctl -u ztd-gateway -f"

ssh:
	gcloud compute ssh $(INSTANCE_NAME) --zone=$(ZONE) --project=$(PROJECT_ID)

destroy:
	terraform destroy -var="project_id=$(PROJECT_ID)" -var="zone=$(ZONE)" -var="instance_name=$(INSTANCE_NAME)"

