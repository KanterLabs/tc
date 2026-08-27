#!/usr/bin/env bash
set -Eeuo pipefail

: "${CLOUDFLARE_API_TOKEN:?CLOUDFLARE_API_TOKEN is required}"
[[ "$CLOUDFLARE_API_TOKEN" != *$'\n'* && "$CLOUDFLARE_API_TOKEN" != *$'\r'* ]] || {
	printf 'CLOUDFLARE_API_TOKEN contains a control character\n' >&2
	exit 1
}
CF_ACCESS_CLIENT_ID=${CF_ACCESS_CLIENT_ID:-}
CF_ACCESS_CLIENT_SECRET=${CF_ACCESS_CLIENT_SECRET:-}
REQUIRE_SERVICE_AUTH_PROBE=${ROADMAP_REQUIRE_SERVICE_AUTH_PROBE:-0}
[[ "$REQUIRE_SERVICE_AUTH_PROBE" = 0 || "$REQUIRE_SERVICE_AUTH_PROBE" = 1 ]] || {
	printf 'ROADMAP_REQUIRE_SERVICE_AUTH_PROBE must be 0 or 1\n' >&2
	exit 1
}
if [[ -n "$CF_ACCESS_CLIENT_ID" || -n "$CF_ACCESS_CLIENT_SECRET" || "$REQUIRE_SERVICE_AUTH_PROBE" = 1 ]]; then
	[[ -n "$CF_ACCESS_CLIENT_ID" && -n "$CF_ACCESS_CLIENT_SECRET" ]] || {
		printf 'CF_ACCESS_CLIENT_ID and CF_ACCESS_CLIENT_SECRET are required for the Service Auth probe\n' >&2
		exit 1
	}
	[[ "$CF_ACCESS_CLIENT_ID" != *$'\n'* && "$CF_ACCESS_CLIENT_ID" != *$'\r'* &&
		"$CF_ACCESS_CLIENT_SECRET" != *$'\n'* && "$CF_ACCESS_CLIENT_SECRET" != *$'\r'* ]] || {
		printf 'Cloudflare Access service credentials contain a control character\n' >&2
		exit 1
	}
fi
command -v curl >/dev/null 2>&1 || { printf 'curl is required\n' >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { printf 'jq is required\n' >&2; exit 1; }

CF_API=https://api.cloudflare.com/client/v4
CF_HEADER_FILE=$(mktemp /tmp/roadmap-cloudflare-header.XXXXXX)
CF_SERVICE_HEADER_FILE=
CF_SERVICE_RESPONSE_HEADERS=
CF_SERVICE_RESPONSE_BODY=
cleanup() {
	rm -f -- "$CF_HEADER_FILE" "$CF_SERVICE_HEADER_FILE" "$CF_SERVICE_RESPONSE_HEADERS" "$CF_SERVICE_RESPONSE_BODY"
}
trap cleanup EXIT
chmod 0600 "$CF_HEADER_FILE"
printf 'Authorization: Bearer %s\n' "$CLOUDFLARE_API_TOKEN" > "$CF_HEADER_FILE"
ACCOUNT_ID=090ae73dce25f4eca9a53ee396fdc916
ZONE_ID=1206ce4daa0fe3c4791f9df9069764f6
PUBLIC_HOST=tc.shanekanterman.dev
PUBLIC_URL=https://tc.shanekanterman.dev
if [[ "${ROADMAP_PUBLIC_ORIGIN+x}" = x && "$ROADMAP_PUBLIC_ORIGIN" != "$PUBLIC_URL" ]]; then
	printf 'ROADMAP_PUBLIC_ORIGIN must be exactly %s\n' "$PUBLIC_URL" >&2
	exit 1
fi
TUNNEL_NAME=roadmap-homelab
UI_APP_NAME='Roadmap owner UI'
API_APP_NAME='Roadmap agents API'
OWNER_POLICY_NAME='Roadmap owner only'
SERVICE_POLICY_NAME='Roadmap agents Service Auth'

cf_request() {
	local method=$1 path=$2 response
	response=$(curl --fail --silent --show-error --proto '=https' --tlsv1.2 \
		--request "$method" --header "@$CF_HEADER_FILE" "$CF_API$path")
	[[ "$(jq -r '.success // false' <<<"$response")" = true ]] || {
		printf 'Cloudflare API request failed\n' >&2
		return 1
	}
	printf '%s' "$response"
}

valid_email() {
	[[ "$1" =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$ ]]
}

owner_email() {
	local configured members
	configured=${ROADMAP_ADMIN_EMAIL:-}
	if [[ -z "$configured" && -f dist/owner.env && ! -L dist/owner.env ]]; then
		configured=$(awk -F= '$1 == "ROADMAP_ADMIN_EMAIL" { print substr($0, index($0, "=") + 1) }' dist/owner.env)
	fi
	if [[ -n "$configured" ]]; then
		valid_email "$configured" || { printf 'Roadmap owner email is invalid\n' >&2; return 1; }
		printf '%s' "$configured"
		return 0
	fi
	members=$(cf_request GET "/accounts/$ACCOUNT_ID/members")
	configured=$(jq -r '[.result[] | select(.status == "accepted") | .user.email] | if length == 1 then .[0] else empty end' <<<"$members")
	valid_email "$configured" || {
		printf 'could not determine the single accepted Roadmap owner email\n' >&2
		return 1
	}
	printf '%s' "$configured"
}

identity_provider_id() {
	local providers id
	providers=$(cf_request GET "/accounts/$ACCOUNT_ID/access/identity_providers")
	id=$(jq -r '[.result[] | select(.type == "onetimepin")] | if length == 1 then .[0].id else empty end' <<<"$providers")
	[[ -n "$id" ]] || {
		printf 'Cloudflare one-time PIN identity provider is missing or ambiguous\n' >&2
		return 1
	}
	printf '%s' "$id"
}

access_team_name() {
	local organization auth_domain
	organization=$(cf_request GET "/accounts/$ACCOUNT_ID/access/organizations")
	auth_domain=$(jq -r '.result.auth_domain // empty' <<<"$organization")
	[[ "$auth_domain" =~ ^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]] || {
		printf 'Cloudflare Access organization domain is invalid\n' >&2
		return 1
	}
	printf '%s' "${auth_domain%%.*}"
}

validate_access_app() {
	local app_id=$1 name=$2 domain=$3 idp=$4 service_401=$5 response audience
	response=$(cf_request GET "/accounts/$ACCOUNT_ID/access/apps/$app_id")
	jq -e --arg name "$name" --arg domain "$domain" --arg idp "$idp" --argjson service401 "$service_401" \
		'.result.name == $name and .result.domain == $domain and .result.type == "self_hosted" and
		 .result.session_duration == "168h" and .result.auto_redirect_to_identity == true and
		 .result.app_launcher_visible == false and (.result.allowed_idps // []) == [$idp] and
		 (.result.service_auth_401_redirect // false) == $service401' <<<"$response" >/dev/null || {
		printf 'Access application differs from the reviewed definition: %s\n' "$domain" >&2
		return 1
	}
	audience=$(jq -r '.result.aud // empty' <<<"$response")
	[[ -n "$audience" && "$audience" != null ]] || {
		printf 'Access application audience is missing: %s\n' "$domain" >&2
		return 1
	}
	printf '%s' "$audience"
}

access_status() {
	local path=$1
	curl --silent --show-error --output /dev/null --max-time 10 --write-out '%{http_code}' \
		"$PUBLIC_URL$path"
}

expect_access() {
	local path=$1 status
	status=$(access_status "$path")
	case "$status" in
		302|303|401) ;;
		*) printf 'expected Cloudflare Access response for %s, got HTTP %s\n' "$path" "$status" >&2; return 1 ;;
	esac
	printf 'access_%s=%s\n' "${path#/}" "$status"
}

service_auth_probe() {
	[[ -n "$CF_ACCESS_CLIENT_ID" && -n "$CF_ACCESS_CLIENT_SECRET" ]] || {
		printf 'service_auth_probe=skipped\n'
		return 0
	}
	CF_SERVICE_HEADER_FILE=$(mktemp /tmp/roadmap-cloudflare-service-header.XXXXXX)
	CF_SERVICE_RESPONSE_HEADERS=$(mktemp /tmp/roadmap-cloudflare-service-response-headers.XXXXXX)
	CF_SERVICE_RESPONSE_BODY=$(mktemp /tmp/roadmap-cloudflare-service-response-body.XXXXXX)
	chmod 0600 "$CF_SERVICE_HEADER_FILE" "$CF_SERVICE_RESPONSE_HEADERS" "$CF_SERVICE_RESPONSE_BODY"
	# Keep the one-time service credentials in a mode-0600 curl config file. The
	# values never appear in argv, logs, or the response diagnostics below.
	printf 'CF-Access-Client-Id: %s\nCF-Access-Client-Secret: %s\n' \
		"$CF_ACCESS_CLIENT_ID" "$CF_ACCESS_CLIENT_SECRET" > "$CF_SERVICE_HEADER_FILE"
	local status content_type request_id expected_request_id=roadmap-service-auth-probe
	status=$(curl --silent --show-error --proto '=https' --tlsv1.2 \
		--output "$CF_SERVICE_RESPONSE_BODY" --dump-header "$CF_SERVICE_RESPONSE_HEADERS" \
		--max-time 15 --write-out '%{http_code}' --header '@'"$CF_SERVICE_HEADER_FILE" \
		--header 'Accept: application/json' --header "X-Request-ID: $expected_request_id" \
		"$PUBLIC_URL/api/v1/roadmap") || {
		printf 'Cloudflare Service Auth probe request failed\n' >&2
		return 1
	}
	content_type=$(awk 'tolower($0) ~ /^content-type:/ { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print; exit }' "$CF_SERVICE_RESPONSE_HEADERS")
	request_id=$(awk 'tolower($0) ~ /^x-request-id:/ { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print; exit }' "$CF_SERVICE_RESPONSE_HEADERS")
	[[ "$status" = 401 ]] || {
		printf 'Service Auth probe expected Roadmap HTTP 401, got %s\n' "$status" >&2
		return 1
	}
	[[ "$content_type" = application/json* ]] || {
		printf 'Service Auth probe did not receive a JSON response from Roadmap\n' >&2
		return 1
	}
	[[ "$request_id" = "$expected_request_id" ]] || {
		printf 'Service Auth probe response did not preserve the Roadmap X-Request-ID\n' >&2
		return 1
	}
	jq -e '(.error.code == "unauthorized") and (.error.message | type == "string")' \
		"$CF_SERVICE_RESPONSE_BODY" >/dev/null || {
		printf 'Service Auth probe response is not Roadmap JSON unauthorized\n' >&2
		return 1
	}
	printf 'service_auth_probe=ok\n'
}

apps=$(cf_request GET "/accounts/$ACCOUNT_ID/access/apps?per_page=100")
ui_id=$(jq -r --arg domain "$PUBLIC_HOST" '[.result[] | select(.domain == $domain)] | if length == 1 then .[0].id else empty end' <<<"$apps")
api_id=$(jq -r --arg domain "$PUBLIC_HOST/api/v1/*" '[.result[] | select(.domain == $domain)] | if length == 1 then .[0].id else empty end' <<<"$apps")
[[ -n "$ui_id" && -n "$api_id" ]] || { printf 'both Roadmap Access applications are required\n' >&2; exit 1; }
idp=$(identity_provider_id)
team=$(access_team_name)
ui_aud=$(validate_access_app "$ui_id" "$UI_APP_NAME" "$PUBLIC_HOST" "$idp" false)
api_aud=$(validate_access_app "$api_id" "$API_APP_NAME" "$PUBLIC_HOST/api/v1/*" "$idp" true)

owner=$(owner_email)

service_tokens=$(cf_request GET "/accounts/$ACCOUNT_ID/access/service_tokens?per_page=100")
service_token_id=$(jq -r --arg name "Roadmap agents" '[.result[] | select(.name == $name)] | if length == 1 then .[0].id else empty end' <<<"$service_tokens")
service_token_expiry=$(jq -r --arg id "$service_token_id" '.result[] | select(.id == $id) | (.expires_at // .expiration_time // empty)' <<<"$service_tokens")
[[ -n "$service_token_id" && "$service_token_expiry" != null && -n "$service_token_expiry" ]] || {
	printf 'Roadmap service token is missing or ambiguous\n' >&2
	exit 1
}
expiry_epoch=$(date -u -d "$service_token_expiry" +%s 2>/dev/null) || { printf 'Roadmap service token expiry is invalid\n' >&2; exit 1; }
now_epoch=$(date -u +%s)
(( expiry_epoch > now_epoch && expiry_epoch <= now_epoch + 366*24*60*60 )) || {
	printf 'Roadmap service token is expired or exceeds the one-year limit\n' >&2
	exit 1
}

ui_policies=$(cf_request GET "/accounts/$ACCOUNT_ID/access/apps/$ui_id/policies")
api_policies=$(cf_request GET "/accounts/$ACCOUNT_ID/access/apps/$api_id/policies")
jq -e --arg name "$OWNER_POLICY_NAME" --arg email "$owner" \
	'([.result[]] | length == 1) and ([.result[] | select(.name == $name and .decision == "allow" and .precedence == 1 and .include == [{email:{email:$email}}])] | length == 1)' <<<"$ui_policies" >/dev/null \
	|| { printf 'UI Access app lacks its exact-owner Allow policy\n' >&2; exit 1; }
jq -e --arg owner_name "$OWNER_POLICY_NAME" --arg service_name "$SERVICE_POLICY_NAME" --arg email "$owner" --arg token "$service_token_id" \
	'([.result[]] | length == 2) and
	 ([.result[] | select(.name == $owner_name or .name == $service_name)] | length == 2) and
	 ([.result[] | select(.name == $owner_name and .decision == "allow" and .precedence == 2 and .include == [{email:{email:$email}}])] | length == 1) and
	 ([.result[] | select(.name == $service_name and .decision == "non_identity" and .precedence == 1 and .include == [{service_token:{token_id:$token}}])] | length == 1)' <<<"$api_policies" >/dev/null \
	|| { printf 'API Access app policy set is not exactly owner Allow plus Service Auth\n' >&2; exit 1; }

tunnel=$(cf_request GET "/accounts/$ACCOUNT_ID/cfd_tunnel?is_deleted=false&per_page=100")
tunnel_id=$(jq -r --arg name "$TUNNEL_NAME" '[.result[] | select(.name == $name)] | if length == 1 then .[0].id // empty else empty end' <<<"$tunnel")
tunnel_status=$(jq -r --arg name "$TUNNEL_NAME" '[.result[] | select(.name == $name)] | if length == 1 then .[0].status // empty else empty end' <<<"$tunnel")
[[ -n "$tunnel_id" && -n "$tunnel_status" ]] || { printf 'Roadmap tunnel is missing or ambiguous\n' >&2; exit 1; }
[[ "$tunnel_status" = healthy ]] || { printf 'expected healthy Roadmap tunnel, got %s\n' "$tunnel_status" >&2; exit 1; }

tunnel_config=$(cf_request GET "/accounts/$ACCOUNT_ID/cfd_tunnel/$tunnel_id/configurations")
jq -e --arg host "$PUBLIC_HOST" --arg team "$team" --arg ui "$ui_aud" --arg api "$api_aud" \
	'(.result.config.ingress // []) as $ingress |
	 ($ingress | type == "array" and length == 2) and
	 ([$ingress[] | select(.hostname == $host)] | length == 1) and
	 ([$ingress[] | select(.service == "http_status:404" and ((.hostname // null) == null))] | length == 1) and
	 (($ingress[0] | keys | sort) == ["hostname", "originRequest", "service"]) and
	 (($ingress[0].originRequest | keys | sort) == ["access"]) and
	 (($ingress[0].originRequest.access | keys | sort) == ["audTag", "required", "teamName"]) and
	 ($ingress[0].hostname == $host) and
	 ($ingress[0].service == "http://127.0.0.1:8080") and
	 ($ingress[0].originRequest.access.required == true) and
	 ($ingress[0].originRequest.access.teamName == $team) and
	 ((($ingress[0].originRequest.access.audTag // []) | sort) == ([$ui, $api] | sort)) and
	 (($ingress[1] | keys | sort) == ["service"]) and
	 ($ingress[1].service == "http_status:404")' <<<"$tunnel_config" >/dev/null \
	|| { printf 'Roadmap tunnel configuration is not exactly the reviewed ingress set\n' >&2; exit 1; }

dns=$(cf_request GET "/zones/$ZONE_ID/dns_records?name=$PUBLIC_HOST")
jq -e --arg host "$PUBLIC_HOST" --arg target "$tunnel_id.cfargotunnel.com" \
	'(.result | type == "array" and length == 1) and
	 (.result[0].name == $host) and
	 (.result[0].type == "CNAME") and
	 (.result[0].content == $target) and
	 (.result[0].proxied == true)' <<<"$dns" >/dev/null \
	|| { printf 'Roadmap DNS record is not exactly the reviewed proxied tunnel CNAME\n' >&2; exit 1; }

# Health and OpenAPI deliberately remain behind Access. This check must fail
# if somebody adds a public bypass to either endpoint.
expect_access /healthz
expect_access /openapi.json
expect_access /
expect_access /api/v1/roadmap
if [[ "$REQUIRE_SERVICE_AUTH_PROBE" = 1 || -n "$CF_ACCESS_CLIENT_ID" || -n "$CF_ACCESS_CLIENT_SECRET" ]]; then
	service_auth_probe
else
	printf 'service_auth_probe=skipped\n'
fi

printf 'live_validation=ok\n'
