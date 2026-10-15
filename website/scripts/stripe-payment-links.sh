#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Licensed under the Business Source License 1.1 — see LICENSE for details.
#
# Creates or reconciles the Stripe products, prices, and Payment Links behind the homepage purchase buttons, then
# writes src/payment-links.json (SKU -> public Payment Link URL). The Worker serves /buy/<sku> from that file.
#
# Usage: scripts/stripe-payment-links.sh [--live]
#   Without --live it runs against Stripe test mode and prints the mapping instead of writing it, so test links
#   never reach the site.
#
# Idempotent: prices are found by lookup key and links by metadata.sku. A changed amount creates a new price that
# takes over the lookup key, and the link for the old price is deactivated and replaced.
set -euo pipefail

MODE=()
if [[ "${1:-}" == "--live" ]]; then MODE=(--live); elif [[ $# -gt 0 ]]; then echo "usage: $0 [--live]" >&2; exit 2; fi

OUTPUT="$(cd "$(dirname "$0")/.." && pwd)/src/payment-links.json"
CONFIRMATION="Payment received — thank you. Your Stripe receipt is on its way, and license delivery follows by email from danny@zerotrustdata.io. Questions: danny@zerotrustdata.io."

# sku|name|description|unit_amount_cents|interval (month, year, or once)
CATALOG=(
  "compliance-cli-pack|ztd Compliance CLI Pack|Commercial production rights for up to 10 enrolled Operators, audit exporter, and L1 Doctrine packs.|9900|month"
  "fleet-50|ztd 50-Host Fleet Pack|Commercial production license for up to 50 Governed Operators and Gateways, with governance certificate.|149900|year"
  "sovereign-250|ztd 250-Host Sovereign Pack|Commercial license for 250 Operators and unlimited Gateways, air-gapped bundles, and custom L1 Doctrine rules.|399900|year"
  "oem|ztd Commercial OEM License|BSL 1.1 commercial exemption with white-label and OEM redistribution rights.|295000|year"
  "diagnostic|Architecture & Red-Team Diagnostic|5-business-day fixed-scope architecture and red-team audit with written remediation plan.|250000|once"
  "sprint|0-to-1 Governance or SRE Buildout|10-business-day forward-deployed governance or SRE implementation sprint.|750000|once"
  "retainer|Principal Architect Retainer|Monthly Tier-4 escalation and async architecture review retainer, including a 50-Host Fleet Pack.|350000|month"
)

# Runs a Stripe API call and fails on an API error, since the CLI exits 0 when Stripe returns one.
api() {
  local out
  out="$(stripe "$1" "$2" "${MODE[@]}" "${@:3}" </dev/null)"
  if jq -e '.error' >/dev/null <<<"$out"; then
    echo "stripe $1 $2 failed: $(jq -r '.error.message' <<<"$out")" >&2
    exit 1
  fi
  printf '%s' "$out"
}

ensure_price() {
  local sku="$1" name="$2" description="$3" amount="$4" interval="$5"
  local lookup="ztd_${sku//-/_}_${interval}" price product
  price="$(api get /v1/prices -d "lookup_keys[]=$lookup" -d active=true -d limit=1 | jq -c '.data[0] // empty')"
  if [[ -n "$price" && "$(jq -r '.unit_amount' <<<"$price")" == "$amount" ]]; then
    jq -r '.id' <<<"$price"
    return
  fi
  if [[ -n "$price" ]]; then
    product="$(jq -r '.product' <<<"$price")"
  else
    product="$(api post /v1/products -c -d "name=$name" -d "description=$description" -d "metadata[sku]=$sku" | jq -r '.id')"
  fi
  local args=(-d "product=$product" -d currency=usd -d "unit_amount=$amount" -d "lookup_key=$lookup" -d transfer_lookup_key=true)
  [[ "$interval" != once ]] && args+=(-d "recurring[interval]=$interval")
  api post /v1/prices -c "${args[@]}" | jq -r '.id'
}

ensure_link() {
  local sku="$1" price="$2" interval="$3" links current
  links="$(api get /v1/payment_links -d active=true -d limit=100)"
  current="$(jq -r --arg sku "$sku" --arg price "$price" \
    '.data[] | select(.metadata.sku == $sku and .metadata.price == $price) | .url' <<<"$links" | head -n1)"
  if [[ -n "$current" ]]; then
    echo "$current"
    return
  fi
  jq -r --arg sku "$sku" '.data[] | select(.metadata.sku == $sku) | .id' <<<"$links" | while read -r stale; do
    api post "/v1/payment_links/$stale" -c -d active=false >/dev/null
  done
  local args=(
    -d "line_items[0][price]=$price" -d "line_items[0][quantity]=1"
    -d billing_address_collection=required -d "tax_id_collection[enabled]=true" -d allow_promotion_codes=true
    -d "after_completion[type]=hosted_confirmation"
    -d "after_completion[hosted_confirmation][custom_message]=$CONFIRMATION"
    -d "metadata[sku]=$sku" -d "metadata[price]=$price"
  )
  [[ "$interval" == once ]] && args+=(-d "invoice_creation[enabled]=true")
  api post /v1/payment_links -c "${args[@]}" | jq -r '.url'
}

result='{}'
for entry in "${CATALOG[@]}"; do
  IFS='|' read -r sku name description amount interval <<<"$entry"
  price="$(ensure_price "$sku" "$name" "$description" "$amount" "$interval")"
  url="$(ensure_link "$sku" "$price" "$interval")"
  echo "$sku -> $url" >&2
  result="$(jq --arg sku "$sku" --arg url "$url" '. + {($sku): $url}' <<<"$result")"
done
if [[ ${#MODE[@]} -eq 0 ]]; then
  jq -S . <<<"$result"
else
  jq -S . <<<"$result" >"$OUTPUT"
  echo "wrote $OUTPUT" >&2
fi
