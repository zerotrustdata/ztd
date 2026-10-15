#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Licensed under the Business Source License 1.1 — see LICENSE for details.
#
# Creates or reconciles the Stripe products, prices, and Payment Links behind the homepage purchase buttons, then
# writes src/payment-links.json (SKU -> public Payment Link URL). The Worker serves /buy/<sku> from that file.
#
# Usage: [STRIPE_API_KEY=rk_live_...] scripts/stripe-payment-links.sh [--live]
#   --live requires STRIPE_API_KEY. Without --live it runs against Stripe test mode and prints the mapping instead of writing it, so test links
#   never reach the site.
#
# Checkout requires acceptance of https://zerotrustdata.io/terms/, which must also be set as the Terms of Service
# URL in the Stripe Dashboard (Settings -> Business -> Public details) for each mode.
#
# Idempotent: prices are found by lookup key and links by metadata.sku. A changed amount creates a new price that
# takes over the lookup key, and the link for the old price is deactivated and replaced. Product names and
# descriptions are updated in place. Bump LINK_REVISION after changing link settings to replace every link.
#
# Deactivated links stop accepting payments, so rerun with --live and deploy the regenerated payment-links.json.
set -euo pipefail
shopt -s inherit_errexit

MODE=() MODE_NAME=test
if [[ "${1:-}" == "--live" ]]; then MODE=(--live) MODE_NAME=live; elif [[ $# -gt 0 ]]; then echo "usage: $0 [--live]" >&2; exit 2; fi
for tool in stripe jq; do
  command -v "$tool" >/dev/null || { echo "$tool is not installed" >&2; exit 1; }
done
# The CLI keeps live keys in the OS keyring, which headless hosts lack, and then blocks on an interactive login.
# STRIPE_API_KEY takes precedence over the keyring, so live runs require it.
if [[ $MODE_NAME == live && ! "${STRIPE_API_KEY:-}" =~ ^(sk|rk)_live_ ]]; then
  echo "--live needs STRIPE_API_KEY set to a live secret or restricted key (rk_live_...) with write access to Products, Prices, and Payment Links" >&2
  exit 2
fi

OUTPUT="$(cd "$(dirname "$0")/.." && pwd)/src/payment-links.json"
LINK_REVISION=3
TERMS_URL="https://zerotrustdata.io/terms/"
OEM_AGREEMENT_URL="https://zerotrustdata.io/terms/oem/"
CONFIRMATION="Payment received — thank you. Your Stripe receipt is on its way. Within one business day, danny@zerotrustdata.io will email your License Certificate, or for services, to agree on scope and a start date."

# sku|name|description|unit_amount_cents|interval (month, year, or once)
CATALOG=(
  "compliance-cli-pack|ztd Compliance CLI Pack|Production license for up to 10 enrolled Operators, with signed compliance report bundles and L1 Doctrine packs.|9900|month"
  "fleet-50|ztd 50-Host Fleet Pack|Production license for up to 50 enrolled Operators and Gateways combined, with async support.|149900|year"
  "sovereign-250|ztd 250-Host Sovereign Pack|Production license for up to 250 enrolled Operators and unlimited Gateways, with air-gapped deployment, custom L1 Doctrine rules, and a topology review.|399900|year"
  "oem|ztd Commercial OEM License|BSL 1.1 commercial exemption with white-label and OEM redistribution rights, under a signed license agreement.|295000|year"
  "diagnostic|Architecture & Red-Team Diagnostic|5-business-day fixed-scope architecture and red-team audit with written remediation plan.|250000|once"
  "sprint|0-to-1 Governance or SRE Buildout|10-business-day forward-deployed governance or SRE implementation sprint.|750000|once"
  "retainer|Principal Architect Retainer|Monthly Tier-4 escalation and async architecture review retainer, including a 50-Host Fleet Pack.|350000|month"
)

# Runs a Stripe API call and fails loudly on anything but a JSON object without an error: the CLI exits 0 when
# Stripe returns an API error, and prints plain text (an expired or missing key, a bad flag) on its own failures.
api() {
  local out status=0
  out="$(stripe "$1" "$2" "${MODE[@]}" "${@:3}" </dev/null)" || status=$?
  if ((status != 0)) || ! jq -e 'type == "object"' >/dev/null 2>&1 <<<"$out"; then
    echo "stripe $1 $2 failed (exit $status): ${out:-no output}" >&2
    exit 1
  fi
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
  if [[ -n "$price" ]]; then
    product="$(jq -r '.product' <<<"$price")"
    api post "/v1/products/$product" -c -d "name=$name" -d "description=$description" >/dev/null
    if [[ "$(jq -r '.unit_amount' <<<"$price")" == "$amount" ]]; then
      jq -r '.id' <<<"$price"
      return
    fi
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
  current="$(jq -r --arg sku "$sku" --arg price "$price" --arg revision "$LINK_REVISION" \
    '.data[] | select(.metadata.sku == $sku and .metadata.price == $price and .metadata.revision == $revision) | .url' \
    <<<"$links" | head -n1)"
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
    -d "consent_collection[terms_of_service]=required"
    -d "custom_text[terms_of_service_acceptance][message]=I agree to the [Commercial License and Services Terms]($TERMS_URL)."
    -d "metadata[sku]=$sku" -d "metadata[price]=$price" -d "metadata[revision]=$LINK_REVISION"
  )
  [[ "$interval" == once ]] && args+=(-d "invoice_creation[enabled]=true")
  [[ "$sku" == oem ]] && args+=(-d "custom_text[submit][message]=After payment, we email the [ztd Commercial OEM License Agreement]($OEM_AGREEMENT_URL) for signature.")
  api post /v1/payment_links -c "${args[@]}" | jq -r '.url'
}

# Command substitutions do not inherit this trap, so a failure anywhere reports once, from the top level.
trap 'status=$?; ((status == 0)) || echo "aborted; $OUTPUT was not updated" >&2' EXIT
echo "reconciling ${#CATALOG[@]} SKUs in Stripe $MODE_NAME mode" >&2
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
