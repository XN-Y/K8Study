#!/usr/bin/env bash
set -euo pipefail

if [[ $# -gt 1 || ( $# -eq 1 && $1 != --full ) ]]; then
  printf 'Usage: bash scripts/check-lab.sh [--full]\n' >&2
  exit 2
fi

k=(kubectl --context kind-go-lab)
lab=("${k[@]}" -n go-lab)

printf 'Checking the node and shipping service\n'
"${k[@]}" wait --for=condition=Ready nodes --all --timeout=30s
"${lab[@]}" rollout status deployment/shipping --timeout=30s
ready=$("${lab[@]}" get endpointslices -l kubernetes.io/service-name=shipping \
  -o jsonpath='{range .items[*].endpoints[*]}{.conditions.ready}{"\n"}{end}')
if ! grep -Fxq true <<<"$ready"; then
  printf 'Shipping Service has no ready endpoints.\n' >&2
  exit 1
fi
curl --noproxy '*' -fsS --max-time 5 'http://127.0.0.1:8088/api/shipping?kg=3' |
  python3 -c '
import json, sys
quote = json.load(sys.stdin)
if quote.get("kg") != 3 or not isinstance(quote.get("price_cny"), int) or not quote.get("version"):
    sys.exit(f"Unexpected shipping response: {quote}")
print("Shipping returned {} CNY for 3 kg ({})".format(quote["price_cny"], quote["version"]))
'

if [[ $# -eq 0 ]]; then
  exit 0
fi

printf 'Checking checkout, Gateways and the preview controller\n'
for workload in checkout prometheus-lab preview-controller; do
  "${lab[@]}" rollout status "deployment/$workload" --timeout=30s
done
"${k[@]}" -n envoy-gateway-system rollout status deployment/envoy-gateway --timeout=30s
"${lab[@]}" wait --for=condition=Programmed gateway/go-lab gateway/go-lab-https --timeout=30s
"${lab[@]}" get httproute go-lab checkout-https -o json | python3 -c '
import json, sys
for route in json.load(sys.stdin)["items"]:
    name = route["metadata"]["name"]
    parents = route.get("status", {}).get("parents", [])
    if not any({"Accepted", "ResolvedRefs"} <= {
        c["type"] for c in parent.get("conditions", []) if c["status"] == "True"
    } for parent in parents):
        sys.exit(f"HTTPRoute conditions not satisfied: {name}")
    print(f"HTTPRoute accepted and references resolved: {name}")
'
"${lab[@]}" get secret checkout-lab-tls -o jsonpath='{.data.tls\.crt}' |
  base64 --decode | openssl x509 -noout -checkend 0
"${k[@]}" wait --for=condition=Established crd/shippingpreviews.lab.example.com --timeout=30s
"${k[@]}" --request-timeout=8s get --raw \
  '/api/v1/namespaces/go-lab/services/checkout/proxy/api/checkout?subtotal_cny=100&kg=3' |
  python3 -c '
import json, sys
reply = json.load(sys.stdin)
if reply["subtotal_cny"] != 100 or reply["total_cny"] != 100 + reply["shipping"]["price_cny"]:
    sys.exit(f"Unexpected checkout response: {reply}")
print("Checkout returned a total of {} CNY".format(reply["total_cny"]))
'

printf 'Checking resource metrics and Prometheus scraping\n'
"${k[@]}" top pods -n go-lab
prometheus=$("${lab[@]}" get pods -l app=prometheus-lab -o json | python3 -c '
import json, sys
for pod in json.load(sys.stdin)["items"]:
    if not pod["metadata"].get("deletionTimestamp") and any(
        c["type"] == "Ready" and c["status"] == "True" for c in pod["status"].get("conditions", [])
    ):
        print(pod["metadata"]["name"])
        break
else:
    sys.exit("No ready Prometheus Pod")
')
"${k[@]}" --request-timeout=8s get --raw \
  "/api/v1/namespaces/go-lab/pods/$prometheus:9090/proxy/api/v1/query?query=up%7Bjob%3D%22checkout%22%7D" |
  python3 -c '
import json, sys
reply = json.load(sys.stdin)
if reply.get("status") != "success" or not any(r["value"][1] == "1" for r in reply["data"]["result"]):
    sys.exit("Prometheus checkout scrape is not up")
print("Prometheus checkout scrape is up")
'
