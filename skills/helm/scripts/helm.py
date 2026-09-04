#!/usr/bin/env python3
"""Track agent work in Helm without exposing credentials."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import stat
import sys
import time
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib import error, parse, request

import helm_session


DEFAULT_BASE_URL = "https://tc.shanekanterman.dev"
DEFAULT_CONFIG = Path("~/.config/helm/credentials.json").expanduser()
LEGACY_CONFIG = Path("~/.config/tc-roadmap/credentials.json").expanduser()
MAX_RESPONSE_BYTES = 4 * 1024 * 1024
MAX_TRANSIENT_RETRIES = 2
MAX_RETRY_DELAY_SECONDS = 5.0
UUID4_PATTERN = re.compile(
    r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$"
)

# Board Audit is intentionally a client-side, read-only projection. Keep the
# limits here independent from the API's larger collection limits so one audit
# result remains useful as agent context even on a busy board.
AUDIT_DEFAULT_STATES = ("backlog", "active")
AUDIT_SEMANTIC_STATES = ("backlog", "ready", "active", "blocked", "completed")
AUDIT_AGENT_STATES = ("working", "waiting", "verifying", "handoff")
AUDIT_PULSE_STALE_SECONDS = 15 * 60
AUDIT_MAX_TEXT = 4000
AUDIT_MAX_AGENT_TEXT = 600
AUDIT_MAX_CRITERION_TEXT = 800
AUDIT_MAX_EVIDENCE_TEXT = 280
AUDIT_MAX_EVIDENCE_ITEMS = 5
AUDIT_MAX_EVIDENCE_REFS = 12


class HelmError(RuntimeError):
    """A safe-to-display Helm client error.

    HTTP failures retain a small amount of structured, non-sensitive context so
    callers can make safe retry decisions without parsing human-facing text.
    """

    def __init__(
        self,
        message: str,
        *,
        status_code: int | None = None,
        error_code: str = "",
        retry_after: str | None = None,
    ) -> None:
        super().__init__(message)
        self.status_code = status_code
        # ``status``/``http_code`` preserve the HTTP status while
        # ``error_code``/``api_code`` preserve the server's machine code.
        self.status = status_code
        self.http_code = status_code
        self.error_code = error_code
        self.api_code = error_code
        self.code = status_code
        self.retry_after = retry_after

    def as_dict(self) -> dict[str, Any]:
        """Return a bounded, machine-readable error without credential data."""

        payload: dict[str, Any] = {
            "code": self.error_code or "client_error",
            "message": str(self)[:300],
        }
        if self.status_code is not None:
            payload["status"] = self.status_code
        if self.retry_after:
            payload["retry_after"] = self.retry_after[:120]
        return payload


# The old exception name is retained for callers that imported the helper
# directly; the canonical public name is HelmError.
RoadmapError = HelmError


@dataclass(frozen=True)
class Config:
    base_url: str
    token: str
    cf_access_client_id: str = ""
    cf_access_client_secret: str = ""


def _credential_path() -> Path:
    canonical = os.environ.get("HELM_CONFIG", "").strip()
    legacy = os.environ.get("TC_ROADMAP_CONFIG", "").strip()
    if canonical and legacy and Path(canonical).expanduser() != Path(legacy).expanduser():
        raise HelmError("conflicting Helm and legacy credential file settings")
    if canonical or legacy:
        return Path(canonical or legacy).expanduser()
    if DEFAULT_CONFIG.exists():
        return DEFAULT_CONFIG
    # Keep the old credential location as a read-only migration fallback.
    return LEGACY_CONFIG


def _read_config_file(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    info = path.stat()
    if info.st_uid != os.getuid():
        raise HelmError(f"credential file is not owned by the current user: {path}")
    if stat.S_IMODE(info.st_mode) & 0o077:
        raise HelmError(f"credential file permissions are too broad: {path}; use mode 0600")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise HelmError(f"cannot read credential file {path}: {type(exc).__name__}") from exc
    if not isinstance(value, dict):
        raise HelmError(f"credential file must contain a JSON object: {path}")
    return value


def _env_alias(environ: dict[str, str] | None, names: tuple[str, ...], label: str) -> str:
    """Resolve one canonical setting while rejecting conflicting aliases."""

    source = os.environ if environ is None else environ
    values: list[tuple[str, str]] = []
    for name in names:
        raw = str(source.get(name, "")).strip()
        if raw:
            # URL aliases commonly differ only by a trailing slash. Treat
            # those as the same origin before checking for a conflict.
            if name.endswith("URL"):
                raw = raw.rstrip("/")
            values.append((name, raw))
    distinct = {value for _name, value in values}
    if len(distinct) > 1:
        raise HelmError(f"conflicting Helm and legacy {label} settings")
    return values[0][1] if values else ""


def _config_value(values: dict[str, Any], names: tuple[str, ...], label: str) -> str:
    """Read canonical/legacy file keys with the same fail-closed rule."""

    present = []
    for name in names:
        value = values.get(name)
        if isinstance(value, str) and value.strip():
            present.append((name, value.strip().rstrip("/") if name.endswith("url") else value.strip()))
    distinct = {value for _name, value in present}
    if len(distinct) > 1:
        raise HelmError(f"conflicting Helm and legacy {label} settings")
    return present[0][1] if present else ""


def load_config() -> Config:
    values = _read_config_file(_credential_path())
    configured_url = _env_alias(None, ("HELM_URL", "TC_ROADMAP_URL"), "URL")
    configured_token = _env_alias(None, ("HELM_TOKEN", "TC_ROADMAP_TOKEN", "ROADMAP_TOKEN"), "token")
    file_url = _config_value(values, ("helm_url", "base_url", "tc_roadmap_url"), "URL")
    file_token = _config_value(values, ("helm_token", "token", "tc_roadmap_token", "roadmap_token"), "token")
    configured_client_id = _env_alias(
        None, ("HELM_CF_ACCESS_CLIENT_ID", "TC_CF_ACCESS_CLIENT_ID"), "Cloudflare client ID"
    )
    configured_client_secret = _env_alias(
        None, ("HELM_CF_ACCESS_CLIENT_SECRET", "TC_CF_ACCESS_CLIENT_SECRET"), "Cloudflare client secret"
    )
    file_client_id = _config_value(
        values, ("helm_cf_access_client_id", "cf_access_client_id", "tc_cf_access_client_id"), "Cloudflare client ID"
    )
    file_client_secret = _config_value(
        values,
        ("helm_cf_access_client_secret", "cf_access_client_secret", "tc_cf_access_client_secret"),
        "Cloudflare client secret",
    )
    # Environment configuration intentionally has precedence over either
    # credential file, while aliases at the same layer must agree.
    base_url = (configured_url or file_url or DEFAULT_BASE_URL).strip().rstrip("/")
    token = (configured_token or file_token).strip()
    client_id = (configured_client_id or file_client_id).strip()
    client_secret = (configured_client_secret or file_client_secret).strip()
    parsed = parse.urlsplit(base_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.username or parsed.password:
        raise HelmError("Helm URL must be an HTTP(S) origin without credentials")
    if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        raise HelmError("Helm URL must not contain a path, query, or fragment")
    if parsed.scheme != "https" and parsed.hostname not in {"127.0.0.1", "localhost", "::1"}:
        raise HelmError("Helm URL must use HTTPS except on loopback")
    if not token:
        raise HelmError("Helm agent token is not configured")
    if bool(client_id) != bool(client_secret):
        raise HelmError("both Cloudflare Access credential fields must be configured together")
    return Config(base_url, token, client_id, client_secret)


class _NoRedirect(request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[no-untyped-def]
        return None


class Client:
    def __init__(self, config: Config) -> None:
        self.config = config

    @staticmethod
    def _retry_safe(method: str, path: str, idempotency_key: str | None) -> bool:
        """Return whether a request can be replayed after a transient error.

        GET is naturally replayable. Normal writes are replayable only when
        they carry the helper's deterministic idempotency key. Heartbeat is a
        deliberately idempotent server-side timestamp touch and therefore is
        safe even though it intentionally has no idempotency key.
        """

        if method.upper() in {"GET", "HEAD", "OPTIONS"}:
            return True
        return bool(idempotency_key) or (
            method.upper() == "POST" and path.rstrip("/").endswith("/heartbeat")
        )

    @staticmethod
    def _retry_delay(headers: dict[str, str], attempt: int) -> float:
        """Choose a bounded delay while preserving the server's hint.

        ``Retry-After`` can be either a number of seconds or an HTTP date. A
        malformed/far-future value falls back to a small exponential delay;
        no caller is ever made to sleep for more than five seconds.
        """

        raw = headers.get("retry-after", "").strip()
        delay: float | None = None
        if raw:
            try:
                delay = max(0.0, float(raw))
            except ValueError:
                try:
                    retry_at = datetime.strptime(raw, "%a, %d %b %Y %H:%M:%S GMT").replace(tzinfo=timezone.utc)
                    delay = max(0.0, (retry_at - datetime.now(timezone.utc)).total_seconds())
                except ValueError:
                    delay = None
        if delay is None:
            delay = min(0.5 * (2**attempt), MAX_RETRY_DELAY_SECONDS)
        return min(delay, MAX_RETRY_DELAY_SECONDS)

    def call(
        self,
        method: str,
        path: str,
        *,
        body: dict[str, Any] | None = None,
        if_match: int | None = None,
        idempotency_key: str | None = None,
    ) -> tuple[Any, dict[str, str]]:
        data = None
        if body is not None:
            data = json.dumps(body, separators=(",", ":")).encode("utf-8")
        target = self.config.base_url + "/api/v1" + path
        retry_safe = self._retry_safe(method, path, idempotency_key)
        for attempt in range(MAX_TRANSIENT_RETRIES + 1):
            headers = {
                "Accept": "application/json",
                "Authorization": f"Bearer {self.config.token}",
                "User-Agent": "helm-skill/1",
            }
            if self.config.cf_access_client_id:
                headers["CF-Access-Client-Id"] = self.config.cf_access_client_id
                headers["CF-Access-Client-Secret"] = self.config.cf_access_client_secret
            if body is not None:
                headers["Content-Type"] = "application/json"
            if if_match is not None:
                headers["If-Match"] = f'"v{if_match}"'
            if idempotency_key:
                headers["Idempotency-Key"] = idempotency_key
            req = request.Request(target, method=method, headers=headers, data=data)
            try:
                with request.build_opener(_NoRedirect).open(req, timeout=15) as response:
                    raw = response.read(MAX_RESPONSE_BYTES + 1)
                    response_headers = {key.lower(): value for key, value in response.headers.items()}
            except error.HTTPError as exc:
                response_headers = (
                    {key.lower(): value for key, value in exc.headers.items()}
                    if exc.headers is not None
                    else {}
                )
                try:
                    raw = exc.read(MAX_RESPONSE_BYTES + 1)
                finally:
                    exc.close()
                if exc.code in {429, 503} and retry_safe and attempt < MAX_TRANSIENT_RETRIES:
                    time.sleep(self._retry_delay(response_headers, attempt))
                    continue
                message, error_code = _error_details(raw)
                message = _redact_secrets(
                    message,
                    (self.config.token, self.config.cf_access_client_id, self.config.cf_access_client_secret),
                )
                raise HelmError(
                    f"Helm returned HTTP {exc.code}: {message}",
                    status_code=exc.code,
                    error_code=error_code,
                    retry_after=response_headers.get("retry-after"),
                ) from exc
            except (error.URLError, TimeoutError, OSError) as exc:
                raise HelmError(f"cannot reach Helm: {type(exc).__name__}") from exc
            if len(raw) > MAX_RESPONSE_BYTES:
                raise HelmError("Helm response exceeded the safe size limit")
            if not raw:
                return None, response_headers
            try:
                return json.loads(raw), response_headers
            except json.JSONDecodeError as exc:
                raise HelmError("Helm returned a non-JSON response") from exc


def _error_message(raw: bytes) -> str:
    return _error_details(raw)[0]


def _error_details(raw: bytes) -> tuple[str, str]:
    try:
        payload = json.loads(raw[:MAX_RESPONSE_BYTES])
    except json.JSONDecodeError:
        return "request failed", ""
    if isinstance(payload, dict):
        error_value = payload.get("error")
        if isinstance(error_value, dict):
            message = error_value.get("message")
            code = error_value.get("code")
            if isinstance(message, str):
                return message[:300], code[:120] if isinstance(code, str) else ""
    return "request failed", ""


def _redact_secrets(value: str, secrets: tuple[str, ...]) -> str:
    """Remove configured credential values from user-visible text."""

    result = value
    for secret in secrets:
        if secret:
            result = result.replace(secret, "[redacted]")
    return result[:MAX_RESPONSE_BYTES]


def _safe_error_for_client(exc: HelmError, client: Client) -> dict[str, Any]:
    payload = exc.as_dict()
    config = getattr(client, "config", None)
    secrets = (
        str(getattr(config, "token", "") or ""),
        str(getattr(config, "cf_access_client_id", "") or ""),
        str(getattr(config, "cf_access_client_secret", "") or ""),
    )
    for field in ("code", "message", "retry_after"):
        value = payload.get(field)
        if isinstance(value, str):
            payload[field] = _redact_secrets(value, secrets)[:300 if field == "message" else 120]
    return payload


def _data(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
        raise HelmError("Helm returned an unexpected collection")
    return [item for item in payload["data"] if isinstance(item, dict)]


def _collection_cursor(payload: Any) -> str:
    """Read a collection cursor without treating null as an endless cursor."""

    if not isinstance(payload, dict):
        raise HelmError("Helm returned an unexpected collection")
    cursor = payload.get("next_cursor", "")
    # Older compatible deployments omitted the terminal cursor. The current
    # contract uses an empty string, but treating omission as terminal keeps a
    # read-only audit useful against those deployments.
    if cursor is None:
        return ""
    if not isinstance(cursor, str):
        raise HelmError("Helm returned an invalid collection cursor")
    return cursor.strip()


def _query_path(path: str, params: list[tuple[str, str]]) -> str:
    query = parse.urlencode(params)
    return path + ("?" + query if query else "")


def _paged_collection(
    client: Client,
    path: str,
    params: list[tuple[str, str]],
    *,
    context: str,
) -> list[dict[str, Any]]:
    """Read every page of a cursor collection using GET only.

    The API's cursors are opaque to clients. We therefore pass each cursor
    through verbatim and only guard against a broken server returning the same
    cursor forever.
    """

    result: list[dict[str, Any]] = []
    cursor = ""
    seen: set[str] = set()
    while True:
        page_params = list(params)
        if cursor:
            page_params.append(("cursor", cursor))
        payload, _ = client.call("GET", _query_path(path, page_params))
        try:
            result.extend(_data(payload))
            next_cursor = _collection_cursor(payload)
        except HelmError as exc:
            raise HelmError(f"invalid {context} collection response") from exc
        if not next_cursor:
            return result
        if next_cursor in seen or next_cursor == cursor:
            raise HelmError(f"{context} collection returned a repeated cursor")
        seen.add(next_cursor)
        cursor = next_cursor


def _paged_collection_with_cursor(
    client: Client,
    path: str,
    params: list[tuple[str, str]],
    *,
    context: str,
    initial_cursor: str = "",
    cursor_param: str = "cursor",
    collect_all: bool = False,
) -> tuple[list[dict[str, Any]], str]:
    """Read an offset collection while retaining its terminal cursor.

    ``cursor`` values are opaque to callers.  This helper only transports the
    exact server value and prevents duplicate query parameters when a caller
    starts from an explicitly supplied cursor.
    """

    result: list[dict[str, Any]] = []
    cursor = initial_cursor.strip()
    seen: set[str] = set()
    while True:
        page_params = [(key, value) for key, value in params if key != cursor_param]
        if cursor:
            page_params.append((cursor_param, cursor))
        payload, _ = client.call("GET", _query_path(path, page_params))
        try:
            result.extend(_data(payload))
            next_cursor = _collection_cursor(payload)
        except HelmError as exc:
            raise HelmError(f"invalid {context} collection response") from exc
        if not collect_all:
            return result, next_cursor
        if not next_cursor:
            return result, ""
        if next_cursor in seen or next_cursor == cursor:
            raise HelmError(f"{context} collection returned a repeated cursor")
        seen.add(next_cursor)
        cursor = next_cursor


def _validate_limit(value: Any, *, default: int = 50) -> int:
    """Validate the API collection limit before making a request."""

    if value is None:
        return default
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise HelmError("limit must be an integer between 1 and 200") from exc
    if parsed < 1 or parsed > 200:
        raise HelmError("limit must be an integer between 1 and 200")
    return parsed


def _validate_nonnegative_int(value: Any, label: str) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise HelmError(f"{label} must be a non-negative integer") from exc
    if parsed < 0:
        raise HelmError(f"{label} must be a non-negative integer")
    return parsed


def _new_operation_id(value: Any = None) -> str:
    """Resolve a new-workflow mutation ID to a UUIDv4.

    Existing Helm/TC compatibility commands intentionally retain their
    deterministic ``helm-<sha256>`` namespace.  New workflow commands use a
    UUIDv4 so the value itself is a valid API idempotency key.  Re-running a
    logical operation is deterministic when the caller supplies the emitted
    UUID again.
    """

    if value is None or not str(value).strip():
        return str(uuid.uuid4())
    candidate = str(value).strip()
    if not UUID4_PATTERN.fullmatch(candidate):
        raise HelmError("operation_id must be a UUIDv4 for this mutation")
    return candidate.lower()


def _new_mutation_idempotency(operation_id: str) -> str:
    """Use one UUIDv4 for one new logical mutation and safe retries."""

    return _new_operation_id(operation_id)


def _derived_uuid4(seed: str) -> str:
    """Derive a deterministic UUIDv4-shaped key for a mutation sub-step."""

    raw = bytearray(hashlib.sha256(seed.encode("utf-8")).digest()[:16])
    raw[6] = (raw[6] & 0x0F) | 0x40
    raw[8] = (raw[8] & 0x3F) | 0x80
    return str(uuid.UUID(bytes=bytes(raw)))


def _command_operation_id(value: Any = None) -> str:
    """Use UUIDv4 by default while retaining explicit legacy operation IDs."""

    if value is None or not str(value).strip():
        return str(uuid.uuid4())
    candidate = str(value).strip()
    if UUID4_PATTERN.fullmatch(candidate):
        return candidate.lower()
    # Existing Helm commands accepted safe human-readable IDs. Keep those
    # deterministic for callers that explicitly continue to use them, while
    # making every newly-created logical operation a UUIDv4 by default.
    return _validate_operation_id(candidate)


def _command_mutation_id(operation_id: str, method: str, path: str, detail: Any = "") -> str:
    """Return a per-mutation UUID key or the legacy deterministic key."""

    if UUID4_PATTERN.fullmatch(operation_id):
        if not isinstance(detail, str):
            detail = _canonical_json(detail)
        seed = "\0".join((operation_id, method.upper().strip(), path.strip(), detail))
        return _derived_uuid4(seed)
    return _mutation_idempotency(operation_id, method, path, detail)


def _safe_actor(actor: Any) -> dict[str, Any]:
    """Keep auth-check output limited to non-secret actor identity fields."""

    if not isinstance(actor, dict):
        return {}
    result: dict[str, Any] = {}
    for field in ("id", "kind", "name", "admin"):
        if field in actor and isinstance(actor[field], (str, bool)):
            result[field] = actor[field]
    for field in ("project_ids", "scopes"):
        values = actor.get(field)
        if isinstance(values, list) and all(isinstance(value, str) for value in values):
            result[field] = [value[:200] for value in values[:200]]
    return result


def _auth_failure_hint(exc: HelmError) -> str:
    if exc.status_code == 401:
        return "Check HELM_URL and HELM_TOKEN; the token may be expired or invalid."
    if exc.status_code == 403:
        return "Check the token scopes and project ceiling, then verify Cloudflare edge credentials if configured."
    if exc.status_code in {429, 503}:
        return "Retry after the server's backoff window; no mutation was attempted by auth-check."
    if exc.status_code is None:
        return "Check HELM_URL, network access, and the API TLS certificate."
    return "Inspect the returned API code and verify the Helm endpoint configuration."


def _audit_states(value: Any) -> tuple[str, ...]:
    if value is None:
        return AUDIT_DEFAULT_STATES
    if isinstance(value, str):
        values: list[Any] = [part for part in value.split(",") if part.strip()]
    elif isinstance(value, (list, tuple)):
        values = list(value)
    else:
        raise HelmError("audit states must be semantic state names")
    states: list[str] = []
    for item in values:
        if not isinstance(item, str):
            raise HelmError("audit states must be semantic state names")
        state = item.strip()
        if state not in AUDIT_SEMANTIC_STATES:
            raise HelmError("audit states must be semantic state names")
        if state not in states:
            states.append(state)
    if not states:
        raise HelmError("audit requires at least one semantic state")
    return tuple(states)


def _audit_page_size(value: Any) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise HelmError("audit page size must be between 1 and 200") from exc
    if parsed < 1 or parsed > 200:
        raise HelmError("audit page size must be between 1 and 200")
    return parsed


def _audit_evidence_limit(value: Any) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise HelmError("audit evidence limit must be between 0 and 5") from exc
    if parsed < 0:
        raise HelmError("audit evidence limit must be between 0 and 5")
    # A caller may ask for more, but the output contract stays bounded.
    return min(parsed, AUDIT_MAX_EVIDENCE_ITEMS)


# Text fields are read-only API data, but descriptions, progress summaries,
# and comments can still contain accidental credentials. Redact common secret
# forms before they become agent context. Evidence comments are omitted when
# they contain a sensitive marker that cannot be safely isolated.
_AUDIT_PRIVATE_KEY = re.compile(
    r"-----BEGIN [^-\n]*PRIVATE KEY-----.*?-----END [^-\n]*PRIVATE KEY-----",
    re.IGNORECASE | re.DOTALL,
)
_AUDIT_BEARER = re.compile(r"\bBearer\s+[A-Za-z0-9._~+/=-]+", re.IGNORECASE)
_AUDIT_SECRET_ASSIGNMENT = re.compile(
    r"(\b(?:[A-Za-z][A-Za-z0-9-]*[_-])*(?:authorization|bearer|token|secret|password|passwd|api[_-]?(?:key|token)|access[_-]?token|client[_-]?(?:secret|token))\b\s*[:=]\s*)([^\s,;]+)",
    re.IGNORECASE,
)
_AUDIT_QUERY_SECRET = re.compile(
    r"([?&](?:[A-Za-z][A-Za-z0-9-]*[_-])*(?:token|secret|password|passwd|api[_-]?(?:key|token)|access[_-]?token|sig|signature)=)[^&#\s]+",
    re.IGNORECASE,
)
_AUDIT_TOKEN_PREFIX = re.compile(r"\b(?:sk|rk|gh[pousr]|xox[baprs])[-_][A-Za-z0-9._-]+", re.IGNORECASE)
_AUDIT_SENSITIVE_WORD = re.compile(
    r"\b[^\s,;]*(?:authorization|bearer|token|secret|password|passwd|api[_-]?(?:key|token)|access[_-]?token|private\s+key)[^\s,;]*",
    re.IGNORECASE,
)


def _audit_redact_text(value: Any, limit: int) -> str:
    if not isinstance(value, str):
        return ""
    text = value.strip()
    if not text:
        return ""
    text = _AUDIT_PRIVATE_KEY.sub("[REDACTED]", text)
    text = _AUDIT_BEARER.sub("Bearer [REDACTED]", text)
    text = _AUDIT_SECRET_ASSIGNMENT.sub(r"\1[REDACTED]", text)
    text = _AUDIT_QUERY_SECRET.sub(r"\1[REDACTED]", text)
    text = _AUDIT_TOKEN_PREFIX.sub("[REDACTED]", text)
    # Remove marker words as a final guard against leaking a marker's value in
    # an otherwise unstructured sentence (for example, "secret abc").
    text = _AUDIT_SENSITIVE_WORD.sub("[REDACTED]", text)
    if len(text) > limit:
        text = text[: max(0, limit - 1)].rstrip() + "…"
    return text


def _audit_safe_identifier(value: Any, limit: int = 128) -> str:
    if not isinstance(value, str):
        return ""
    value = value.strip()
    if not value:
        return ""
    # IDs and operation IDs are contract-safe identifiers. Still apply a
    # length bound, and never copy a value containing control characters.
    if any(ord(char) < 0x20 or ord(char) == 0x7F for char in value):
        return ""
    return value[:limit]


def _audit_timestamp(value: Any) -> datetime | None:
    if not isinstance(value, str) or not value.strip():
        return None
    try:
        parsed = datetime.fromisoformat(value.strip().replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


def _audit_liveness(work: dict[str, Any] | None, now: datetime) -> tuple[str, bool]:
    if work is None:
        return "missing", False
    stale = work.get("stale")
    if isinstance(stale, bool):
        return ("stale" if stale else "fresh"), stale
    updated = _audit_timestamp(work.get("updated_at"))
    if updated is not None:
        stale = (now - updated).total_seconds() >= AUDIT_PULSE_STALE_SECONDS
        return ("stale" if stale else "fresh"), stale
    return "unknown", False


def _audit_safe_agent_work(work: Any) -> dict[str, Any] | None:
    if not isinstance(work, dict):
        return None
    result: dict[str, Any] = {}
    for field in ("operation_id", "actor_id", "state", "started_at", "updated_at"):
        value = work.get(field)
        if isinstance(value, str) and value.strip():
            result[field] = _audit_safe_identifier(value)
    for field in ("phase", "summary", "next_action"):
        value = _audit_redact_text(work.get(field), AUDIT_MAX_AGENT_TEXT)
        if value:
            result[field] = value
    refs = work.get("checkpoint_refs")
    if isinstance(refs, list):
        safe_refs = []
        for ref in refs[:AUDIT_MAX_EVIDENCE_REFS]:
            safe_ref = _audit_redact_text(ref, 128)
            if safe_ref:
                safe_refs.append(safe_ref)
        if safe_refs:
            result["checkpoint_refs"] = safe_refs
    for field in ("checkpoint_completed", "checkpoint_total"):
        value = work.get(field)
        if isinstance(value, int) and not isinstance(value, bool):
            result[field] = value
    for field in ("stale", "action_needed"):
        value = work.get(field)
        if isinstance(value, bool):
            result[field] = value
    return result


def _audit_claim(task: dict[str, Any], now: datetime) -> dict[str, Any]:
    owner = task.get("claimed_by")
    expires = task.get("claim_expires_at")
    nested = task.get("claim")
    if isinstance(nested, dict):
        if not isinstance(owner, str) or not owner.strip():
            owner = nested.get("owner_id", nested.get("claimed_by"))
        if not isinstance(expires, str) or not expires.strip():
            expires = nested.get("expires_at", nested.get("claim_expires_at"))
    owner_id = _audit_safe_identifier(owner)
    expires_at = _audit_safe_identifier(expires)
    parsed_expiry = _audit_timestamp(expires_at)
    if not owner_id:
        status = "unclaimed"
    elif parsed_expiry is None:
        status = "unknown"
    elif parsed_expiry > now:
        status = "active"
    else:
        status = "expired"
    return {
        "owner_id": owner_id or None,
        "expires_at": expires_at or None,
        "status": status,
        "active": status == "active",
    }


def _audit_goal(description: str) -> str:
    for line in description.splitlines():
        match = re.match(r"^\s*goal\s*:\s*(.*?)\s*$", line, re.IGNORECASE)
        if match and match.group(1):
            return _audit_redact_text(match.group(1), AUDIT_MAX_TEXT)
    paragraphs = [part.strip() for part in re.split(r"\n\s*\n", description) if part.strip()]
    if paragraphs:
        return _audit_redact_text(paragraphs[0], AUDIT_MAX_TEXT)
    return ""


def _audit_acceptance_criteria(description: str) -> list[str]:
    result: list[str] = []
    in_acceptance_section = False
    for raw_line in description.splitlines():
        line = raw_line.strip()
        if re.match(r"^acceptance criteria\s*:?\s*$", line, re.IGNORECASE):
            in_acceptance_section = True
            continue
        checked = re.match(r"^[-*+]\s+\[[ xX]\]\s+(.+?)\s*$", line)
        bullet = re.match(r"^[-*+]\s+(.+?)\s*$", line)
        value = checked.group(1) if checked else (bullet.group(1) if in_acceptance_section and bullet else "")
        if value:
            safe_value = _audit_redact_text(value, AUDIT_MAX_CRITERION_TEXT)
            if safe_value and len(result) < AUDIT_MAX_EVIDENCE_REFS:
                result.append(safe_value)
            continue
        if in_acceptance_section and line and not line.startswith(("-", "*", "+")):
            # A subsequent heading starts a different section.
            in_acceptance_section = False
    return result


def _audit_current_column(task: dict[str, Any], columns: dict[str, dict[str, Any]]) -> dict[str, Any]:
    column_id = task.get("column_id")
    if not isinstance(column_id, str) or not column_id.strip():
        nested = task.get("column")
        if isinstance(nested, dict):
            column_id = nested.get("id")
    column_id = _audit_safe_identifier(column_id)
    column = columns.get(column_id, {})
    semantic = column.get("semantic_state")
    if not isinstance(semantic, str) or not semantic.strip():
        semantic = task.get("semantic_state")
    if not isinstance(semantic, str) or not semantic.strip():
        nested = task.get("column")
        semantic = nested.get("semantic_state") if isinstance(nested, dict) else ""
    name = column.get("name")
    if not isinstance(name, str) or not name.strip():
        name = task.get("column_name")
    if not isinstance(name, str) or not name.strip():
        nested = task.get("column")
        name = nested.get("name") if isinstance(nested, dict) else ""
    return {
        "id": column_id or None,
        "name": _audit_redact_text(name, 200) or None,
        "semantic_state": semantic.strip() if isinstance(semantic, str) and semantic.strip() else None,
    }


def _audit_confidence_label(score: float) -> str:
    if score >= 0.8:
        return "high"
    if score >= 0.6:
        return "medium"
    return "low"


def _audit_classification(
    semantic: str | None,
    claim: dict[str, Any],
    work: dict[str, Any] | None,
    liveness: str,
    completed_at: Any,
) -> tuple[str, str | None, float, str, list[str]]:
    """Return a conservative verdict and destination for one task."""

    state = work.get("state") if isinstance(work, dict) else None
    warnings: list[str] = []
    if liveness == "stale":
        warnings.append("agent pulse is stale (warning only)")
    elif liveness == "missing":
        warnings.append("agent pulse is missing (warning only)")
    elif liveness == "unknown":
        warnings.append("agent pulse liveness is unknown (warning only)")
    if claim.get("status") == "expired":
        warnings.append("claim lease is expired")
    elif claim.get("status") == "unknown":
        warnings.append("claim lease is not verifiable")
    if state in {"waiting", "handoff"}:
        warnings.append(f"agent work is {state}")

    if semantic not in AUDIT_SEMANTIC_STATES:
        return "needs_attention", semantic, 0.25, "current semantic column is unavailable or unknown", warnings

    fresh_active_work = (
        claim.get("status") == "active"
        and state in {"working", "verifying"}
        and liveness == "fresh"
    )
    if semantic != "active" and fresh_active_work:
        return (
            "move_proposed",
            "active",
            0.92,
            "an active claim and fresh working pulse indicate this task belongs in active",
            warnings,
        )

    if semantic == "active":
        if fresh_active_work:
            return "correct", "active", 0.96, "active claim and fresh working pulse match the active column", warnings
        if state in {"waiting", "handoff"}:
            return "needs_attention", "active", 0.86, f"active work is {state}; keep the task active and inspect the handoff", warnings
        if claim.get("status") in {"active", "expired", "unknown"}:
            return "needs_attention", "active", 0.74, "active placement needs an owner or a current agent pulse", warnings
        if liveness in {"stale", "missing", "unknown"}:
            return "needs_attention", "active", 0.68, "active placement has no current agent pulse; this is a warning only", warnings
        return "needs_attention", "active", 0.62, "active placement is not supported by a current claim or pulse", warnings

    if claim.get("status") == "active" or state in {"working", "verifying", "waiting", "handoff"}:
        return "needs_attention", semantic, 0.78, "work signals do not match this non-active column", warnings
    if liveness == "stale":
        return "needs_attention", semantic, 0.65, "stale agent pulse needs review; it does not prove abandonment", warnings
    if semantic == "completed" and completed_at:
        return "correct", semantic, 0.95, "completion metadata matches the completed column", warnings
    # Missing pulses are expected for unclaimed backlog work. Keep the warning
    # in the machine context without proposing a move based on absence.
    if semantic == "backlog" and claim.get("status") == "unclaimed" and work is None:
        return "correct", semantic, 0.93, "unclaimed work is in the backlog; no active pulse is required", warnings
    return "correct", semantic, 0.9, "no contradictory placement signal was observed", warnings


def _audit_evidence_from_comments(comments: list[dict[str, Any]], limit: int) -> tuple[list[dict[str, Any]], list[str]]:
    if limit <= 0:
        return [], []
    # The API returns comments oldest-first. Keep only the newest bounded
    # window while preserving newest-first order for agent context.
    evidence: list[dict[str, Any]] = []
    for comment in comments[-limit:][::-1]:
        comment_id = _audit_safe_identifier(comment.get("id"))
        if not comment_id:
            continue
        body = comment.get("body", comment.get("text"))
        excerpt = _audit_redact_text(body, AUDIT_MAX_EVIDENCE_TEXT)
        entry: dict[str, Any] = {
            "kind": "comment",
            "id": comment_id,
            "created_at": _audit_safe_identifier(comment.get("created_at")),
        }
        actor_id = _audit_safe_identifier(comment.get("actor_id"))
        if actor_id:
            entry["actor_id"] = actor_id
        if excerpt:
            entry["excerpt"] = excerpt
        evidence.append(entry)
    return evidence, []


def _audit_rubric() -> dict[str, Any]:
    return {
        "version": 1,
        "agent_work_states": list(AUDIT_AGENT_STATES),
        "allowed_verdicts": ["correct", "needs_attention", "move_proposed"],
        "confidence": {
            "type": "number",
            "range": [0.0, 1.0],
            "labels": {"high": ">=0.80", "medium": "0.60-0.79", "low": "<0.60"},
        },
        "rules": [
            {
                "verdict": "move_proposed",
                "when": "A non-active task has an active claim and a fresh working or verifying pulse.",
                "suggested_semantic_state": "active",
            },
            {
                "verdict": "correct",
                "when": "The current column is consistent with claims, live work, and completion metadata.",
                "suggested_semantic_state": "current",
            },
            {
                "verdict": "needs_attention",
                "when": "Signals conflict, a claim is unverifiable, or active work lacks a current owner/pulse.",
                "suggested_semantic_state": "current",
            },
        ],
        "constraints": [
            "A stale or missing pulse is a warning only; never infer completion or abandonment from it.",
            "Do not claim, renew, release, move, complete, block, comment, or emit activity during an audit.",
            "The first release evaluates semantic column placement only and never reorders numeric positions.",
            "Use evidence references and bounded excerpts; do not copy credentials or unrestricted narrative.",
        ],
    }


def _audit_error_context(exc: HelmError) -> dict[str, Any]:
    """Return bounded machine context for a failed audit API call."""

    result: dict[str, Any] = {}
    if exc.error_code:
        result["code"] = exc.error_code[:120]
    if exc.status_code is not None:
        result["status"] = exc.status_code
    return result


def _audit_column_id(value: Any) -> str:
    if isinstance(value, dict):
        value = value.get("id", value.get("column_id"))
    return _audit_safe_identifier(value)


def _audit_semantic_value(value: Any) -> str:
    if isinstance(value, dict):
        value = value.get("semantic_state", value.get("state"))
    return value.strip() if isinstance(value, str) else ""


def _audit_submission_ref(value: Any) -> str:
    """Normalize one evidence reference to the server's safe-ref grammar."""

    value = _audit_safe_identifier(value, 512)
    if not value or not re.fullmatch(r"[A-Za-z0-9/][A-Za-z0-9._:/+~-]*", value):
        return ""
    lowered = value.casefold()
    if lowered.startswith(("javascript:", "data:")):
        return ""
    if _AUDIT_SENSITIVE_WORD.search(value):
        return ""
    return value


def _audit_submission_items(raw: Any) -> list[Any]:
    """Extract findings/tasks from an audit JSON context or direct list."""

    value = raw
    if isinstance(value, (str, Path)):
        if str(value) == "-":
            encoded = sys.stdin.read()
        else:
            try:
                encoded = Path(value).read_text(encoding="utf-8")
            except (OSError, UnicodeError) as exc:
                raise HelmError("cannot read audit submission input") from exc
        try:
            value = json.loads(encoded)
        except json.JSONDecodeError as exc:
            raise HelmError("audit submission input is not valid JSON") from exc
    if isinstance(value, dict):
        if isinstance(value.get("tasks"), list):
            value = value["tasks"]
        elif isinstance(value.get("findings"), list):
            value = value["findings"]
        elif isinstance(value.get("audit"), dict):
            return _audit_submission_items(value["audit"])
    if not isinstance(value, list):
        raise HelmError("audit submission input must contain a findings list")
    return value


def _audit_submission_finding(item: Any) -> tuple[dict[str, Any] | None, str | None]:
    if not isinstance(item, dict):
        return None, "finding is not an object"
    task_id = _audit_safe_identifier(item.get("task_id", item.get("id")))
    version = item.get("captured_version", item.get("version"))
    if not task_id or not isinstance(version, int) or isinstance(version, bool) or version <= 0:
        return None, "finding requires a task ID and positive captured version"
    source = item.get("source_column", item.get("source_column_id"))
    if not source:
        source = item.get("current_column")
    source_column = _audit_column_id(source)
    if not source_column:
        return None, "finding requires a source column ID"
    verdict = item.get("verdict")
    if verdict not in {"correct", "needs_attention", "move_proposed"}:
        return None, "finding verdict is invalid"
    destination: str | None = None
    if verdict == "move_proposed":
        destination_value = item.get("proposed_semantic_destination", item.get("suggested_semantic_state"))
        destination = _audit_semantic_value(destination_value)
        if destination not in AUDIT_SEMANTIC_STATES:
            return None, "move proposal requires a valid semantic destination"
    confidence = item.get("confidence")
    if isinstance(confidence, bool):
        confidence = None
    try:
        confidence = float(confidence)
    except (TypeError, ValueError):
        confidence = None
    if confidence is None or not math.isfinite(confidence) or confidence < 0 or confidence > 1:
        return None, "finding confidence must be between 0 and 1"
    reason = _audit_redact_text(item.get("reason"), 2000)
    if not reason:
        return None, "finding reason is required"
    refs: list[str] = []
    raw_refs = item.get("evidence_refs", [])
    if isinstance(raw_refs, list):
        for ref in raw_refs[:AUDIT_MAX_EVIDENCE_REFS]:
            safe_ref = _audit_submission_ref(ref)
            if safe_ref and safe_ref not in refs:
                refs.append(safe_ref)
    body: dict[str, Any] = {
        "task_id": task_id,
        "captured_version": version,
        "source_column": source_column,
        "verdict": verdict,
        "confidence": confidence,
        "reason": reason,
        "evidence_refs": refs,
        # Submission never imports approval state from a file. Human review is
        # a separate versioned API action, while an idempotent replay of this
        # exact pending finding is handled by the server.
        "review_state": "pending",
    }
    if destination is not None:
        body["proposed_semantic_destination"] = destination
    return body, None


def _audit_run_id(payload: Any) -> str:
    if not isinstance(payload, dict):
        raise HelmError("Helm returned an unexpected audit run")
    audit_id = _audit_safe_identifier(payload.get("id"))
    if not audit_id:
        raise HelmError("Helm returned an unexpected audit run")
    return audit_id


def _reconcile_destination_columns(columns: dict[str, dict[str, Any]]) -> dict[str, dict[str, Any]]:
    result: dict[str, dict[str, Any]] = {}
    ordered = sorted(
        columns.values(),
        key=lambda column: (
            column.get("position") if isinstance(column.get("position"), (int, float)) else 0,
            _audit_safe_identifier(column.get("id")),
        ),
    )
    for column in ordered:
        semantic = _audit_semantic_value(column.get("semantic_state"))
        if semantic in AUDIT_SEMANTIC_STATES and semantic not in result:
            result[semantic] = column
    return result


def _reconcile_action_for_destination(semantic: str) -> str | None:
    return {
        "active": "claim_or_resume",
        "blocked": "block",
        "completed": "complete",
    }.get(semantic)


def _idempotency(operation_id: str, action: str, detail: str = "") -> str:
    seed = "\0".join((operation_id.strip(), action, detail)).encode("utf-8")
    # Keep the mutation namespace stable so a migration cannot duplicate a
    # previously submitted operation for the same task and operation ID.
    return "helm-" + hashlib.sha256(seed).hexdigest()


def _mutation_idempotency(operation_id: str, method: str, path: str, detail: Any = "") -> str:
    """Build a deterministic key scoped to one mutation target.

    Operation IDs are intentionally caller-owned and may be reused for a
    whole session. Including the HTTP method/path and canonical body detail
    prevents a retry for one task or endpoint from replaying a different
    mutation that happens to use the same operation ID.
    """

    if not isinstance(detail, str):
        detail = _canonical_json(detail)
    target = method.upper().strip() + " " + path.strip()
    return _idempotency(operation_id, target, detail)


def _validate_operation_id(operation_id: Any) -> str:
    """Apply the server's operation ID contract before any network write."""

    if not isinstance(operation_id, str):
        raise HelmError("operation_id must be between 1 and 128 safe identifier characters")
    value = operation_id.strip()
    if not value or len(value) > 128:
        raise HelmError("operation_id must be between 1 and 128 safe identifier characters")
    ascii_alnum = lambda char: ("a" <= char <= "z") or ("A" <= char <= "Z") or ("0" <= char <= "9")
    if not (ascii_alnum(value[0]) and all(ascii_alnum(char) or char in "-_.:/" for char in value)):
        raise HelmError("operation_id must be between 1 and 128 safe identifier characters")
    return value


def _task(client: Client, reference: str) -> dict[str, Any]:
    payload, _ = client.call("GET", "/tasks/" + parse.quote(reference, safe=""))
    if not isinstance(payload, dict) or not isinstance(payload.get("version"), int):
        raise HelmError("Helm returned an unexpected task")
    return payload


def _project(client: Client, reference: str) -> dict[str, Any]:
    projects, _ = _paged_collection_with_cursor(
        client,
        "/projects",
        [("limit", "200")],
        context="projects",
        collect_all=True,
    )
    folded = reference.casefold()
    for project in projects:
        candidates = (project.get("id"), project.get("key"), project.get("slug"), project.get("name"))
        if any(isinstance(item, str) and item.casefold() == folded for item in candidates):
            return project
    raise HelmError(f"project not found: {reference}")


def _audit_columns(client: Client, project_id: str, page_size: int) -> dict[str, dict[str, Any]]:
    path = f"/projects/{parse.quote(project_id, safe='')}/columns"
    columns = _paged_collection(client, path, [("limit", str(page_size))], context="columns")
    return {
        column_id: column
        for column in columns
        if (column_id := _audit_safe_identifier(column.get("id")))
        and column.get("project_id", project_id) == project_id
    }


def _audit_tasks(client: Client, project_id: str, states: tuple[str, ...], page_size: int) -> list[dict[str, Any]]:
    path = f"/projects/{parse.quote(project_id, safe='')}/tasks"
    tasks: list[dict[str, Any]] = []
    seen_ids: set[str] = set()
    for state in states:
        for task in _paged_collection(
            client,
            path,
            [("state", state), ("limit", str(page_size))],
            context=f"{state} task",
        ):
            task_id = _audit_safe_identifier(task.get("id"))
            # A task should belong to exactly one semantic state. Deduplicate
            # defensively when a deployment changes a board while pages are
            # being read or when a compatible server ignores a state filter.
            if task_id and task_id in seen_ids:
                continue
            if task_id:
                seen_ids.add(task_id)
            tasks.append(task)
    return tasks


def _audit_comments(client: Client, task_id: str, limit: int) -> tuple[list[dict[str, Any]], str | None]:
    if limit <= 0:
        return [], None
    path = f"/tasks/{parse.quote(task_id, safe='')}/comments"
    # Keep only the newest comments in memory while still traversing every
    # page. Comments are optional evidence: a caller with task-read access may
    # have a compatible deployment that does not expose this route, in which
    # case the audit remains useful and reports a bounded warning.
    comments: list[dict[str, Any]] = []
    cursor = ""
    seen: set[str] = set()
    # Request only the bounded evidence window per page. Traversing the
    # opaque cursor still lets us retain the newest comments without loading
    # older narrative into memory.
    params = [("limit", str(min(limit, 200)))]
    try:
        while True:
            page_params = list(params)
            if cursor:
                page_params.append(("cursor", cursor))
            payload, _ = client.call("GET", _query_path(path, page_params))
            comments.extend(_data(payload))
            if len(comments) > limit:
                comments = comments[-limit:]
            next_cursor = _collection_cursor(payload)
            if not next_cursor:
                return comments, None
            if next_cursor in seen or next_cursor == cursor:
                return comments, "recent evidence pagination returned a repeated cursor"
            seen.add(next_cursor)
            cursor = next_cursor
    except HelmError as exc:
        code = exc.error_code or (str(exc.status_code) if exc.status_code else "unavailable")
        # Do not copy a server error message into audit context; it may contain
        # task text or deployment details outside the bounded evidence schema.
        return comments, f"recent evidence unavailable ({code})"


def _audit_task_context(
    task: dict[str, Any],
    columns: dict[str, dict[str, Any]],
    evidence: list[dict[str, Any]],
    evidence_warning: str | None,
    now: datetime,
) -> dict[str, Any]:
    task_id = _audit_safe_identifier(task.get("id"))
    task_key = _audit_safe_identifier(task.get("key"))
    version = task.get("version")
    if not isinstance(version, int) or isinstance(version, bool):
        version = None
    description = task.get("description") if isinstance(task.get("description"), str) else ""
    current_column = _audit_current_column(task, columns)
    claim = _audit_claim(task, now)
    raw_work = task.get("agent_work")
    work = _audit_safe_agent_work(raw_work)
    liveness, derived_stale = _audit_liveness(raw_work if isinstance(raw_work, dict) else None, now)
    if work is not None and "stale" not in work and liveness != "unknown":
        work["stale"] = derived_stale
    verdict, destination, confidence, reason, warnings = _audit_classification(
        current_column.get("semantic_state"),
        claim,
        work,
        liveness,
        task.get("completed_at"),
    )
    if evidence_warning:
        warnings.append(evidence_warning)

    evidence_refs: list[str] = []
    if isinstance(work, dict):
        operation_id = _audit_safe_identifier(work.get("operation_id"))
        if operation_id:
            evidence_refs.append("agent-work:" + operation_id)
        refs = work.get("checkpoint_refs")
        if isinstance(refs, list):
            for ref in refs:
                safe_ref = _audit_safe_identifier(ref)
                if safe_ref:
                    evidence_refs.append("checkpoint:" + safe_ref)
    for entry in evidence:
        comment_id = _audit_safe_identifier(entry.get("id"))
        if comment_id:
            evidence_refs.append("comment:" + comment_id)
    evidence_refs = evidence_refs[:AUDIT_MAX_EVIDENCE_REFS]

    result: dict[str, Any] = {
        "id": task_id or None,
        "key": task_key or None,
        "version": version,
        "current_column": current_column,
        "title": _audit_redact_text(task.get("title"), AUDIT_MAX_TEXT),
        "description": _audit_redact_text(description, AUDIT_MAX_TEXT),
        "goal": _audit_goal(description),
        "acceptance_criteria": _audit_acceptance_criteria(description),
        "priority": task.get("priority") if isinstance(task.get("priority"), str) else None,
        "claim": claim,
        "agent_work": work,
        "liveness": {
            "state": liveness,
            "updated_at": work.get("updated_at") if isinstance(work, dict) else None,
            "stale": derived_stale,
        },
        "evidence": evidence,
        "evidence_refs": evidence_refs,
        "verdict": verdict,
        "suggested_semantic_state": destination,
        "confidence": round(confidence, 2),
        "confidence_label": _audit_confidence_label(confidence),
        "reason": _audit_redact_text(reason, 500),
        "warnings": warnings,
    }
    return result


def _session_store() -> helm_session.StateStore | None:
    """Return the current session store, or ``None`` when no ID is exposed."""

    try:
        return helm_session.StateStore.from_env()
    except helm_session.SessionStateError as exc:
        _state_warning(exc)
        return None


def _state_warning(exc: BaseException) -> None:
    # Storage is advisory for the network CLI. Never turn an otherwise
    # successful Helm mutation into a failure, and never print a path or
    # caller-provided metadata in the warning.
    print(helm_session.warning(exc), file=sys.stderr)


def _task_identity(task: Any, project_id: str = "") -> tuple[str, str, str] | None:
    if not isinstance(task, dict):
        return None
    task_id = task.get("id")
    if not isinstance(task_id, str) or not task_id.strip():
        return None
    task_key = task.get("key", "")
    if not isinstance(task_key, str):
        task_key = ""
    resolved_project = task.get("project_id")
    if not isinstance(resolved_project, str) or not resolved_project.strip():
        resolved_project = project_id
    if not isinstance(resolved_project, str) or not resolved_project.strip():
        return None
    return task_id.strip(), task_key.strip(), resolved_project.strip()


def _record_session_start(task: Any, project_id: str, operation_id: str, *, snapshot_ready: bool = False) -> None:
    store = _session_store()
    if store is None:
        return
    identity = _task_identity(task, project_id)
    if identity is None:
        _state_warning(helm_session.SessionStateError("task identity is unavailable"))
        return
    task_id, task_key, resolved_project = identity
    try:
        store.save(
            helm_session.SessionState(
                task_id=task_id,
                task_key=task_key,
                project_id=resolved_project,
                operation_id=operation_id,
                agent_state="working",
                last_progress_at=helm_session.timestamp(),
                snapshot_ready=snapshot_ready,
            )
        )
    except helm_session.SessionStateError as exc:
        _state_warning(exc)


def _record_session_progress(task: Any, previous: Any, payload: dict[str, Any]) -> None:
    store = _session_store()
    if store is None:
        return
    project_id = ""
    if isinstance(task, dict) and isinstance(task.get("project_id"), str):
        project_id = task["project_id"]
    if not project_id and isinstance(previous, dict) and isinstance(previous.get("project_id"), str):
        project_id = previous["project_id"]
    identity = _task_identity(task, project_id) or _task_identity(previous, project_id)
    if identity is None:
        return
    task_id, task_key, resolved_project = identity
    try:
        store.save(
            helm_session.SessionState(
                task_id=task_id,
                task_key=task_key,
                project_id=resolved_project,
                operation_id=str(payload.get("operation_id", "")),
                agent_state=str(payload.get("state", "working")),
                checkpoint_completed=payload.get("checkpoint_completed"),
                checkpoint_total=payload.get("checkpoint_total"),
                last_progress_at=helm_session.timestamp(),
                snapshot_ready=True,
            )
        )
    except helm_session.SessionStateError as exc:
        _state_warning(exc)


def _record_session_heartbeat(task: Any, args: argparse.Namespace) -> None:
    store = _session_store()
    if store is None:
        return
    try:
        current = store.load()
        if current is None or current.operation_id != args.operation_id.strip():
            return
        response_task = task
        if isinstance(task, dict) and isinstance(task.get("task"), dict):
            response_task = task["task"]
        response_id = response_task.get("id") if isinstance(response_task, dict) else None
        reference = str(args.task).strip()
        if isinstance(response_id, str) and response_id.strip():
            if current.task_id != response_id.strip():
                return
        elif reference not in {current.task_id, current.task_key}:
            return
        store.update(last_heartbeat_at=helm_session.timestamp())
    except helm_session.SessionStateError as exc:
        _state_warning(exc)


def _clear_matching_session(task: Any, operation_id: str) -> None:
    store = _session_store()
    if store is None:
        return
    if not isinstance(task, dict) or not isinstance(task.get("id"), str):
        return
    try:
        store.clear_matching(task_id=task["id"], operation_id=operation_id)
    except helm_session.SessionStateError as exc:
        _state_warning(exc)


def cmd_audit(client: Client, args: argparse.Namespace) -> Any:
    """Build a bounded, read-only Board Audit context.

    Every network request made by this command is a GET. In particular, an
    audit never claims, renews, moves, comments on, or publishes progress for
    a task; the returned rubric is for an agent or human to review.
    """

    requested_states = getattr(args, "states", None)
    if requested_states is None:
        requested_states = getattr(args, "state", None)
    states = _audit_states(requested_states)
    requested_page_size = getattr(args, "page_size", None)
    if requested_page_size is None:
        requested_page_size = getattr(args, "limit", 200)
    page_size = _audit_page_size(requested_page_size)
    evidence_limit = _audit_evidence_limit(getattr(args, "evidence_limit", AUDIT_MAX_EVIDENCE_ITEMS))
    project = _project(client, args.project)
    project_id = _audit_safe_identifier(project.get("id"))
    if not project_id:
        raise HelmError("Helm returned an unexpected project")
    columns = _audit_columns(client, project_id, page_size)
    tasks = _audit_tasks(client, project_id, states, page_size)
    now = datetime.now(timezone.utc)
    audited: list[dict[str, Any]] = []
    for task in tasks:
        task_id = _audit_safe_identifier(task.get("id"))
        if not task_id:
            # The collection contract requires IDs. Ignore malformed rows so a
            # single bad row cannot produce an unaddressable recommendation.
            continue
        comments, evidence_warning = _audit_comments(client, task_id, evidence_limit)
        evidence, _ = _audit_evidence_from_comments(comments, evidence_limit)
        audited.append(_audit_task_context(task, columns, evidence, evidence_warning, now))

    return {
        "project": project.get("key") if isinstance(project.get("key"), str) else project_id,
        "project_id": project_id,
        "states": list(states),
        "read_only": True,
        "tasks": audited,
        "rubric": _audit_rubric(),
    }


def cmd_submit(client: Client, args: argparse.Namespace) -> Any:
    """Persist agent-reviewed findings as one durable audit run.

    Submission is intentionally separate from ``audit``. The scan remains a
    GET-only operation, while this explicit command records the agent's
    semantic judgment and then closes the run.
    """

    operation_id = _validate_operation_id(args.operation_id)
    project = _project(client, args.project)
    project_id = _audit_safe_identifier(project.get("id"))
    if not project_id:
        raise HelmError("Helm returned an unexpected project")
    scope = _audit_redact_text(getattr(args, "scope", "board"), 200)
    if not scope:
        raise HelmError("audit submission scope is required")
    raw_input = getattr(args, "findings", None)
    if raw_input is None:
        for input_name in ("input_path", "findings_file", "input_file"):
            raw_input = getattr(args, input_name, None)
            if raw_input is not None:
                break
    items = _audit_submission_items(raw_input)
    normalized: list[dict[str, Any]] = []
    local_results: list[dict[str, Any]] = []
    seen_tasks: set[str] = set()
    for item in items:
        finding, error_message = _audit_submission_finding(item)
        if finding is None:
            local_results.append({"status": "skipped", "reason": error_message or "invalid finding"})
            continue
        task_id = str(finding["task_id"])
        if task_id in seen_tasks:
            local_results.append({"task_id": task_id, "status": "skipped", "reason": "duplicate task finding"})
            continue
        seen_tasks.add(task_id)
        normalized.append(finding)

    requested_audit = _audit_safe_identifier(getattr(args, "audit", None))
    if requested_audit:
        run_path = "/audits/" + parse.quote(requested_audit, safe="")
        run, _ = client.call("GET", run_path)
        if not isinstance(run, dict):
            raise HelmError("Helm returned an unexpected audit run")
        if _audit_safe_identifier(run.get("project_id")) != project_id:
            raise HelmError("queued audit belongs to a different project")
        if run.get("status") not in {"queued", "running"}:
            raise HelmError("audit run is already finalized")
        audit_id = _audit_run_id(run)
    else:
        create_path = f"/projects/{parse.quote(project_id, safe='')}/audits"
        create_body = {"scope": scope}
        run, _ = client.call(
            "POST",
            create_path,
            body=create_body,
            idempotency_key=_mutation_idempotency(operation_id, "POST", create_path, create_body),
        )
        audit_id = _audit_run_id(run)
    submitted = list(local_results)
    append_errors = 0
    for finding in normalized:
        path = f"/audits/{parse.quote(audit_id, safe='')}/findings"
        try:
            response, _ = client.call(
                "POST",
                path,
                body=finding,
                idempotency_key=_mutation_idempotency(operation_id, "POST", path, finding),
            )
            finding_id = _audit_safe_identifier(response.get("id")) if isinstance(response, dict) else ""
            submitted.append({"task_id": finding["task_id"], "finding_id": finding_id or None, "status": "appended"})
        except HelmError as exc:
            append_errors += 1
            submitted.append({"task_id": finding["task_id"], "status": "error", "error": _audit_error_context(exc)})

    requested_status = getattr(args, "status", "complete")
    if requested_status not in {"complete", "partial", "failed"}:
        raise HelmError("audit submission status must be complete, partial, or failed")
    terminal_status = requested_status
    if append_errors or any(item.get("status") == "skipped" for item in local_results):
        if terminal_status == "complete":
            terminal_status = "partial"
    finalize_path = f"/audits/{parse.quote(audit_id, safe='')}/finalize"
    finalize_body = {"status": terminal_status}
    finalized, _ = client.call(
        "POST",
        finalize_path,
        body=finalize_body,
        idempotency_key=_mutation_idempotency(operation_id, "POST", finalize_path, finalize_body),
    )
    final_status = finalized.get("status", terminal_status) if isinstance(finalized, dict) else terminal_status
    return {
        "audit_id": audit_id,
        "project": project.get("key") if isinstance(project.get("key"), str) else project_id,
        "status": final_status,
        "findings": submitted,
        "appended": sum(1 for item in submitted if item.get("status") == "appended"),
        "errors": sum(1 for item in submitted if item.get("status") == "error"),
        "skipped": sum(1 for item in submitted if item.get("status") == "skipped"),
    }


def cmd_reconcile(client: Client, args: argparse.Namespace) -> Any:
    """Preview or explicitly apply approved, unchanged audit move proposals."""

    requested_audit = getattr(args, "audit", None)
    if requested_audit is None:
        requested_audit = getattr(args, "audit_id", None)
    audit_id = _audit_safe_identifier(requested_audit)
    if not audit_id:
        raise HelmError("audit ID is required")
    page_size = _audit_page_size(getattr(args, "page_size", getattr(args, "limit", 200)))
    apply_changes = bool(getattr(args, "apply", False))
    operation_id = getattr(args, "operation_id", "")
    if not operation_id:
        operation_id = "audit-reconcile-" + audit_id
    operation_id = _validate_operation_id(operation_id)

    run_payload, _ = client.call("GET", "/audits/" + parse.quote(audit_id, safe=""))
    if not isinstance(run_payload, dict):
        raise HelmError("Helm returned an unexpected audit run")
    project_id = _audit_safe_identifier(run_payload.get("project_id"))
    if not project_id:
        raise HelmError("audit run has no project")
    run_status = run_payload.get("status") if isinstance(run_payload.get("status"), str) else ""
    review_ready = run_status in {"complete", "partial"}
    findings_path = "/audits/" + parse.quote(audit_id, safe="") + "/findings"
    findings = _paged_collection(client, findings_path, [("limit", str(page_size))], context="audit finding")
    columns = _audit_columns(client, project_id, page_size)
    by_semantic = _reconcile_destination_columns(columns)
    now = datetime.now(timezone.utc)
    results: list[dict[str, Any]] = []
    for finding in findings:
        finding_id = _audit_safe_identifier(finding.get("id")) or None
        task_id = _audit_safe_identifier(finding.get("task_id"))
        result: dict[str, Any] = {
            "finding_id": finding_id,
            "task_id": task_id or None,
            "status": "skipped",
            "review_state": finding.get("review_state") if isinstance(finding.get("review_state"), str) else None,
            "verdict": finding.get("verdict") if isinstance(finding.get("verdict"), str) else None,
        }
        if not task_id:
            result["reason"] = "finding has no task ID"
            results.append(result)
            continue
        if not review_ready:
            result["reason"] = "audit run must be complete or partial before reconciliation"
            result["action_required"] = "wait_for_audit" if run_status in {"queued", "running"} else "rerun_audit"
            results.append(result)
            continue
        try:
            current, _ = client.call("GET", "/tasks/" + parse.quote(task_id, safe=""))
            if not isinstance(current, dict) or not isinstance(current.get("version"), int):
                raise HelmError("unexpected task")
        except HelmError as exc:
            result["reason"] = "current task unavailable"
            result["error"] = _audit_error_context(exc)
            result["action_required"] = "refresh_task"
            results.append(result)
            continue

        current_version = current.get("version")
        current_column_id = _audit_safe_identifier(current.get("column_id"))
        if current.get("project_id") and current.get("project_id") != project_id:
            result["reason"] = "current task is outside the audit project"
            result["action_required"] = "inspect_scope"
            results.append(result)
            continue
        source_column_id = _audit_column_id(finding.get("source_column", finding.get("source_column_id")))
        captured_version = finding.get("captured_version")
        changed = bool(finding.get("changed_since_audit")) or (
            not isinstance(captured_version, int)
            or current_version != captured_version
            or not source_column_id
            or current_column_id != source_column_id
        )
        result.update(
            {
                "captured_version": captured_version if isinstance(captured_version, int) else None,
                "current_version": current_version,
                "source_column_id": source_column_id or None,
                "current_column_id": current_column_id or None,
                "changed_since_audit": changed,
            }
        )
        if changed:
            result["reason"] = "task changed since audit; refresh the finding before reconciling"
            result["action_required"] = "rerun_audit"
            results.append(result)
            continue
        if finding.get("review_state") != "approved":
            result["reason"] = "finding is not approved"
            result["action_required"] = "review_finding"
            results.append(result)
            continue
        if finding.get("verdict") != "move_proposed":
            result["reason"] = "finding is not a move proposal"
            results.append(result)
            continue

        destination_semantic = _audit_semantic_value(finding.get("proposed_semantic_destination"))
        result["destination_semantic_state"] = destination_semantic or None
        lifecycle_action = _reconcile_action_for_destination(destination_semantic)
        if lifecycle_action:
            result["reason"] = f"{destination_semantic} requires its lifecycle action"
            result["action_required"] = lifecycle_action
            results.append(result)
            continue
        destination = by_semantic.get(destination_semantic)
        destination_id = _audit_safe_identifier(destination.get("id")) if destination else ""
        if destination_semantic not in {"backlog", "ready"} or not destination_id:
            result["reason"] = "destination semantic column is unavailable"
            result["action_required"] = "review_destination"
            results.append(result)
            continue
        result["destination_column_id"] = destination_id
        result["current_column_semantic_state"] = _audit_semantic_value(
            columns.get(current_column_id, {}).get("semantic_state")
        ) or None
        if destination_id == current_column_id:
            result["reason"] = "task is already in the proposed destination column"
            results.append(result)
            continue
        current_semantic = _audit_semantic_value(columns.get(current_column_id, {}).get("semantic_state"))
        if current_semantic == "completed" or current.get("completed_at"):
            result["reason"] = "completed tasks require an explicit lifecycle action"
            result["action_required"] = "reopen"
            results.append(result)
            continue
        claim = _audit_claim(current, now)
        if claim["status"] == "active":
            result["reason"] = "task has an active claim; release it before moving"
            result["action_required"] = "release_claim"
            result["claim"] = claim
            results.append(result)
            continue
        if claim["status"] == "unknown":
            result["reason"] = "task claim lease is not verifiable"
            result["action_required"] = "inspect_claim"
            result["claim"] = claim
            results.append(result)
            continue

        move_path = "/tasks/" + parse.quote(task_id, safe="") + "/move"
        reason = _audit_redact_text(finding.get("reason"), 2000) or "Approved Board Audit move proposal"
        body = {
            "destination_column_id": destination_id,
            "expected_source_column_id": source_column_id,
            "source": "board_audit",
            "reason": reason,
        }
        result["request"] = {
            "path": move_path,
            "if_match_version": current_version,
            "body": body,
        }
        if not apply_changes:
            result["status"] = "preview"
            result["reason"] = "approved unchanged move proposal; apply explicitly to mutate"
            results.append(result)
            continue
        try:
            moved, _ = client.call(
                "POST",
                move_path,
                body=body,
                if_match=current_version,
                idempotency_key=_mutation_idempotency(operation_id, "POST", move_path, body),
            )
            result["status"] = "applied"
            result["reason"] = "approved move applied"
            if isinstance(moved, dict):
                result["resulting_version"] = moved.get("version")
        except HelmError as exc:
            result["status"] = "conflicted" if exc.status_code == 409 else "error"
            result["reason"] = "task changed during reconciliation" if exc.status_code == 409 else "move was not applied"
            result["error"] = _audit_error_context(exc)
        results.append(result)

    summary = {
        "total": len(results),
        "preview": sum(1 for item in results if item.get("status") == "preview"),
        "applied": sum(1 for item in results if item.get("status") == "applied"),
        "skipped": sum(1 for item in results if item.get("status") == "skipped"),
        "conflicted": sum(1 for item in results if item.get("status") == "conflicted"),
        "errors": sum(1 for item in results if item.get("status") == "error"),
    }
    return {
        "audit_id": audit_id,
        "project_id": project_id,
        "audit_status": run_status or None,
        "apply": apply_changes,
        "read_only": not apply_changes,
        "operation_id": operation_id,
        "summary": summary,
        "findings": results,
    }


def cmd_projects(client: Client, args: argparse.Namespace) -> Any:
    limit = _validate_limit(getattr(args, "limit", 200), default=200)
    cursor = str(getattr(args, "cursor", "") or "").strip()
    projects, next_cursor = _paged_collection_with_cursor(
        client,
        "/projects",
        [("limit", str(limit))],
        context="projects",
        initial_cursor=cursor,
        collect_all=bool(getattr(args, "all", False)),
    )
    selected = [{key: item.get(key) for key in ("id", "key", "slug", "name")} for item in projects]
    return {"projects": selected, "next_cursor": next_cursor}


def cmd_tasks(client: Client, args: argparse.Namespace) -> Any:
    project = _project(client, args.project)
    limit = _validate_limit(getattr(args, "limit", 200), default=200)
    params: list[tuple[str, str]] = [("limit", str(limit))]
    if getattr(args, "query", ""):
        params.append(("q", str(args.query)))
    tasks, next_cursor = _paged_collection_with_cursor(
        client,
        f"/projects/{parse.quote(str(project['id']), safe='')}/tasks",
        params,
        context="tasks",
        initial_cursor=str(getattr(args, "cursor", "") or "").strip(),
        collect_all=bool(getattr(args, "all", False)),
    )
    return {"project": project.get("key"), "tasks": tasks, "next_cursor": next_cursor}


def cmd_auth_check(client: Client, _args: argparse.Namespace) -> Any:
    """Validate the configured agent credential without mutating Helm."""

    try:
        # ``/auth/status`` intentionally treats a bearer header as an edge
        # navigation hint rather than authenticating it.  ``/auth/me`` is the
        # protected, non-mutating identity check for API clients.
        payload, _ = client.call("GET", "/auth/me")
    except HelmError as exc:
        return {
            "ok": False,
            "authenticated": False,
            "error": {**_safe_error_for_client(exc, client), "hint": _auth_failure_hint(exc)},
        }
    if not isinstance(payload, dict):
        error = HelmError("Helm returned an unexpected auth identity")
        return {
            "ok": False,
            "authenticated": False,
            "error": {
                **_safe_error_for_client(error, client),
                "hint": "Retry against a Helm API endpoint that supports /auth/me.",
            },
        }
    return {"ok": True, "authenticated": True, "actor": _safe_actor(payload)}


def _events_page(client: Client, after: int, project: str, limit: int) -> tuple[list[dict[str, Any]], str]:
    params: list[tuple[str, str]] = [("after", str(after))]
    if project:
        params.append(("project", project))
    params.append(("limit", str(limit)))
    payload, _ = client.call("GET", _query_path("/events", params))
    try:
        return _data(payload), _collection_cursor(payload)
    except HelmError as exc:
        raise HelmError("invalid events collection response") from exc


def cmd_events(client: Client, args: argparse.Namespace) -> Any:
    """Poll the append-only event feed using its monotonic cursor."""

    after = _validate_nonnegative_int(getattr(args, "after", 0), "after")
    limit = _validate_limit(getattr(args, "limit", 50))
    project = str(getattr(args, "project", "") or "").strip()
    collect_all = bool(getattr(args, "all", False))
    events: list[dict[str, Any]] = []
    seen: set[int] = set()
    while True:
        page, next_cursor = _events_page(client, after, project, limit)
        events.extend(page)
        if not collect_all or not next_cursor:
            return {"data": events, "next_cursor": "" if collect_all else next_cursor}
        next_after = _validate_nonnegative_int(next_cursor, "event next_cursor")
        if next_after <= after or next_after in seen:
            raise HelmError("events collection returned a repeated cursor")
        seen.add(next_after)
        after = next_after


def _timeline_page(
    client: Client,
    path: str,
    *,
    before: str,
    kind: str,
    limit: int,
) -> tuple[list[dict[str, Any]], str]:
    params: list[tuple[str, str]] = []
    if before:
        params.append(("before", before))
    if kind:
        params.append(("kind", kind))
    params.append(("limit", str(limit)))
    payload, _ = client.call("GET", _query_path(path, params))
    try:
        return _data(payload), _collection_cursor(payload)
    except HelmError as exc:
        raise HelmError("invalid timeline collection response") from exc


def cmd_timeline(client: Client, args: argparse.Namespace) -> Any:
    """Read task or project activity while preserving opaque keyset cursors."""

    task = str(getattr(args, "task", "") or "").strip()
    project = str(getattr(args, "project", "") or "").strip()
    if bool(task) == bool(project):
        raise HelmError("exactly one of --task or --project is required")
    if task:
        path = "/tasks/" + parse.quote(task, safe="") + "/timeline"
    else:
        path = "/projects/" + parse.quote(project, safe="") + "/timeline"
    before = str(getattr(args, "before", "") or "").strip()
    kind = str(getattr(args, "kind", "") or "").strip()
    limit = _validate_limit(getattr(args, "limit", 50))
    collect_all = bool(getattr(args, "all", False))
    items: list[dict[str, Any]] = []
    seen: set[str] = set()
    while True:
        page, next_cursor = _timeline_page(client, path, before=before, kind=kind, limit=limit)
        items.extend(page)
        if not collect_all or not next_cursor:
            return {"data": items, "next_cursor": "" if collect_all else next_cursor}
        if next_cursor in seen or next_cursor == before:
            raise HelmError("timeline collection returned a repeated cursor")
        seen.add(next_cursor)
        before = next_cursor


def _task_action_with_uuid(
    client: Client,
    args: argparse.Namespace,
    action: str,
    body: dict[str, Any] | None,
) -> Any:
    operation_id = _new_operation_id(getattr(args, "operation_id", None))
    current = _task(client, str(getattr(args, "task", "")))
    path = "/tasks/" + parse.quote(str(current["id"]), safe="") + "/" + action
    request_kwargs: dict[str, Any] = {
        "if_match": current["version"],
        "idempotency_key": _new_mutation_idempotency(operation_id),
    }
    if body is not None:
        request_kwargs["body"] = body
    payload, _ = client.call("POST", path, **request_kwargs)
    return {"task": payload, "operation_id": operation_id}


def cmd_renew(client: Client, args: argparse.Namespace) -> Any:
    """Renew only the current owner's active lease through the exact route."""

    lease_seconds = getattr(args, "lease_seconds", 1800)
    return _task_action_with_uuid(client, args, "renew", {"lease_seconds": lease_seconds})


def cmd_release(client: Client, args: argparse.Namespace) -> Any:
    """Release the current owner's active lease through the exact route."""

    result = _task_action_with_uuid(client, args, "release", None)
    if isinstance(result, dict):
        _clear_matching_session(result.get("task"), str(result.get("operation_id", "")))
    return result


def cmd_dependency_list(client: Client, args: argparse.Namespace) -> Any:
    task = _task(client, str(getattr(args, "task", "")))
    path = "/tasks/" + parse.quote(str(task["id"]), safe="") + "/dependencies"
    payload, headers = client.call("GET", path)
    if not isinstance(payload, dict):
        raise HelmError("Helm returned an unexpected dependency collection")
    result = dict(payload)
    result["task"] = task.get("key", task["id"])
    etag = None
    if isinstance(headers, dict):
        etag = next((value for key, value in headers.items() if str(key).lower() == "etag"), None)
    if etag:
        result["etag"] = etag
    return result


def cmd_dependency_add(client: Client, args: argparse.Namespace) -> Any:
    prerequisite = str(getattr(args, "prerequisite", "") or "").strip()
    if not prerequisite:
        raise HelmError("prerequisite must not be empty")
    return _task_action_with_uuid(client, args, "dependencies", {"prerequisite": prerequisite})


def cmd_dependency_remove(client: Client, args: argparse.Namespace) -> Any:
    prerequisite = str(getattr(args, "prerequisite", "") or "").strip()
    if not prerequisite:
        raise HelmError("prerequisite must not be empty")
    operation_id = _new_operation_id(getattr(args, "operation_id", None))
    current = _task(client, str(getattr(args, "task", "")))
    path = (
        "/tasks/"
        + parse.quote(str(current["id"]), safe="")
        + "/dependencies/"
        + parse.quote(prerequisite, safe="")
    )
    payload, _ = client.call(
        "DELETE",
        path,
        if_match=current["version"],
        idempotency_key=_new_mutation_idempotency(operation_id),
    )
    return {"task": payload, "operation_id": operation_id}


def _issue_query_params(args: argparse.Namespace) -> list[tuple[str, str]]:
    params: list[tuple[str, str]] = []
    fields = (
        ("project", "project"),
        ("state", "state"),
        ("column", "column"),
        ("priority", "priority"),
        ("severity", "severity"),
        ("label", "label"),
        ("assignee", "assignee"),
        ("reporter", "reporter"),
        ("resolution", "resolution"),
        ("agent_state", "agent_state"),
        ("query", "q"),
        ("updated_after", "updated_after"),
    )
    for source, target in fields:
        value = getattr(args, source, None)
        if value is not None and str(value).strip():
            params.append((target, str(value).strip()))
    action_needed = getattr(args, "action_needed", None)
    if action_needed is not None:
        if isinstance(action_needed, bool):
            params.append(("action_needed", "true" if action_needed else "false"))
        else:
            value = str(action_needed).strip().lower()
            if value not in {"true", "false"}:
                raise HelmError("action_needed must be true or false")
            params.append(("action_needed", value))
    return params


def cmd_issues(client: Client, args: argparse.Namespace) -> Any:
    params = _issue_query_params(args)
    params.append(("limit", str(_validate_limit(getattr(args, "limit", 50)))))
    items, next_cursor = _paged_collection_with_cursor(
        client,
        "/issues",
        params,
        context="issues",
        initial_cursor=str(getattr(args, "cursor", "") or "").strip(),
        collect_all=bool(getattr(args, "all", False)),
    )
    return {"data": items, "next_cursor": next_cursor}


def _optional_text(args: argparse.Namespace, name: str) -> str | None:
    value = getattr(args, name, None)
    if value is None:
        return None
    normalized = str(value).strip()
    return normalized or None


def cmd_bug_report(client: Client, args: argparse.Namespace) -> Any:
    title = _optional_text(args, "title")
    actual_behavior = _optional_text(args, "actual_behavior")
    if not title or not actual_behavior:
        raise HelmError("title and actual_behavior are required for a bug report")
    operation_id = _new_operation_id(getattr(args, "operation_id", None))
    project = _project(client, str(getattr(args, "project", "")))
    project_id = str(project["id"])
    bug: dict[str, Any] = {"actual_behavior": actual_behavior}
    for source in ("expected_behavior", "reproduction_steps", "environment", "affected_version", "severity"):
        value = _optional_text(args, source)
        if value:
            bug[source] = value
    body: dict[str, Any] = {"title": title, "kind": "bug", "bug": bug}
    for source, target in (("description", "description"), ("priority", "priority"), ("column", "column_id"), ("assignee", "assignee_id")):
        value = _optional_text(args, source)
        if value:
            body[target] = value
    path = "/projects/" + parse.quote(project_id, safe="") + "/tasks"
    task, _ = client.call(
        "POST",
        path,
        body=body,
        idempotency_key=_new_mutation_idempotency(operation_id),
    )
    return {"task": task, "operation_id": operation_id}


def cmd_bug_triage(client: Client, args: argparse.Namespace) -> Any:
    severity = _optional_text(args, "severity")
    if not severity:
        raise HelmError("severity is required for bug triage")
    body: dict[str, Any] = {"severity": severity}
    for source, target in (("priority", "priority"), ("assignee", "assignee_id"), ("column", "column_id")):
        value = _optional_text(args, source)
        if value:
            body[target] = value
    return _task_action_with_uuid(client, args, "triage", body)


def cmd_bug_resolve(client: Client, args: argparse.Namespace) -> Any:
    resolution = _optional_text(args, "resolution")
    if not resolution:
        raise HelmError("resolution is required for bug resolve")
    body: dict[str, Any] = {"resolution": resolution}
    for source, target in (("duplicate_of", "duplicate_of"), ("note", "note")):
        value = _optional_text(args, source)
        if value:
            body[target] = value
    return _task_action_with_uuid(client, args, "resolve", body)


def cmd_bug_duplicate(client: Client, args: argparse.Namespace) -> Any:
    duplicate_of = _optional_text(args, "duplicate_of")
    if not duplicate_of:
        raise HelmError("duplicate_of is required when resolving a duplicate bug")
    body = {"resolution": "duplicate", "duplicate_of": duplicate_of}
    note = _optional_text(args, "note")
    if note:
        body["note"] = note
    return _task_action_with_uuid(client, args, "resolve", body)


def cmd_bug_reopen(client: Client, args: argparse.Namespace) -> Any:
    reason = _optional_text(args, "reason")
    if not reason:
        raise HelmError("reason is required for bug reopen")
    return _task_action_with_uuid(client, args, "reopen", {"reason": reason})


def cmd_start(client: Client, args: argparse.Namespace) -> Any:
    args.operation_id = _command_operation_id(getattr(args, "operation_id", None))
    project = _project(client, args.project)
    project_id = str(project["id"])
    columns, _ = client.call("GET", f"/projects/{parse.quote(project_id, safe='')}/columns?limit=200")
    active = next((item for item in _data(columns) if item.get("semantic_state") == "active"), None)
    if not active:
        raise HelmError(f"project has no active column: {args.project}")
    description = "Goal: " + args.goal.strip()
    if args.step:
        description += "\n\nCheckpoints\n" + "\n".join(f"- [ ] {step.strip()}" for step in args.step)
    create_path = f"/projects/{parse.quote(project_id, safe='')}/tasks"
    create_body = {
        "title": args.title.strip(),
        "description": description,
        "column_id": active["id"],
        "priority": args.priority,
    }
    created, _ = client.call(
        "POST",
        create_path,
        body=create_body,
        idempotency_key=_command_mutation_id(args.operation_id, "POST", create_path, create_body),
    )
    if not isinstance(created, dict) or not isinstance(created.get("version"), int):
        raise HelmError("Helm returned an unexpected created task")
    claim_path = "/tasks/" + parse.quote(str(created["id"]), safe="") + "/claim"
    claim_body = {"lease_seconds": args.lease_seconds}
    claimed, _ = client.call(
        "POST",
        claim_path,
        body=claim_body,
        if_match=created["version"],
        idempotency_key=_command_mutation_id(args.operation_id, "POST", claim_path, claim_body),
    )
    if not isinstance(claimed, dict) or not isinstance(claimed.get("version"), int):
        raise HelmError("Helm returned an unexpected claimed task")
    _record_session_start(claimed, project_id, args.operation_id)
    return {"task": claimed, "operation_id": args.operation_id}


def cmd_backlog(client: Client, args: argparse.Namespace) -> Any:
    args.operation_id = _command_operation_id(getattr(args, "operation_id", None))
    project = _project(client, args.project)
    project_id = str(project["id"])
    columns, _ = client.call("GET", f"/projects/{parse.quote(project_id, safe='')}/columns?limit=200")
    backlog = next((item for item in _data(columns) if item.get("semantic_state") == "backlog"), None)
    if not backlog:
        raise HelmError(f"project has no backlog column: {args.project}")
    description = "Goal: " + args.goal.strip()
    if args.step:
        description += "\n\nAcceptance criteria\n" + "\n".join(f"- [ ] {step.strip()}" for step in args.step)
    create_path = f"/projects/{parse.quote(project_id, safe='')}/tasks"
    create_body = {"title": args.title.strip(), "description": description, "column_id": backlog["id"], "priority": args.priority}
    created, _ = client.call(
        "POST",
        create_path,
        body=create_body,
        idempotency_key=_command_mutation_id(args.operation_id, "POST", create_path, create_body),
    )
    if not isinstance(created, dict) or not isinstance(created.get("version"), int):
        raise HelmError("Helm returned an unexpected created task")
    return {"task": created, "operation_id": args.operation_id}


def cmd_resume(client: Client, args: argparse.Namespace) -> Any:
    args.operation_id = _command_operation_id(getattr(args, "operation_id", None))
    current = _task(client, args.task)
    # The claim endpoint is intentionally used for both an unclaimed task and
    # a same-owner reclaim. The server treats a same-owner POST /claim as a
    # lease renewal, which makes a retry safe after a lost response and avoids
    # making a race-prone claim/renew decision from a stale GET.
    task_id = str(current["id"])
    claim_path = "/tasks/" + parse.quote(task_id, safe="") + "/claim"
    claim_body = {"lease_seconds": args.lease_seconds}
    claimed, _ = client.call(
        "POST",
        claim_path,
        body=claim_body,
        if_match=current["version"],
        idempotency_key=_command_mutation_id(args.operation_id, "POST", claim_path, claim_body),
    )
    if not isinstance(claimed, dict) or not isinstance(claimed.get("version"), int):
        raise HelmError("Helm returned an unexpected claimed task")

    # Claiming does not alter board placement. Resolve the project's semantic
    # active column and move the task with an optimistic PATCH only when the
    # current column is not already active. A claim conflict from another actor
    # raises before this block, so a foreign active claim is never moved.
    project_id = str(current.get("project_id") or claimed.get("project_id") or "")
    if not project_id:
        raise HelmError("Helm returned an unexpected task project")
    current_column = claimed.get("column_id", current.get("column_id"))
    current_semantic = claimed.get("semantic_state", current.get("semantic_state"))
    if current_semantic is None:
        for candidate in (claimed, current):
            nested_column = candidate.get("column") if isinstance(candidate, dict) else None
            if isinstance(nested_column, dict) and nested_column.get("semantic_state") is not None:
                current_semantic = nested_column.get("semantic_state")
                break
    if current_semantic == "active":
        moved = claimed
    else:
        columns_path = f"/projects/{parse.quote(project_id, safe='')}/columns?limit=200"
        columns, _ = client.call("GET", columns_path)
        active = next((item for item in _data(columns) if item.get("semantic_state") == "active"), None)
        if not active or not isinstance(active.get("id"), str) or not active["id"].strip():
            raise HelmError("project has no active column")

        active_id = active["id"]
        if current_column == active_id:
            moved = claimed
        else:
            patch_path = "/tasks/" + parse.quote(task_id, safe="")
            patch_body = {"column_id": active_id}
            moved, _ = client.call(
                "PATCH",
                patch_path,
                body=patch_body,
                if_match=claimed["version"],
                idempotency_key=_command_mutation_id(args.operation_id, "PATCH", patch_path, patch_body),
            )
            if not isinstance(moved, dict) or not isinstance(moved.get("version"), int):
                raise HelmError("Helm returned an unexpected moved task")
    snapshot_ready = False
    for candidate in (moved, claimed):
        if not isinstance(candidate, dict) or not isinstance(candidate.get("agent_work"), dict):
            continue
        work = candidate["agent_work"]
        if work.get("operation_id") != args.operation_id:
            continue
        claimed_by = candidate.get("claimed_by")
        work_actor = work.get("actor_id")
        if isinstance(claimed_by, str) and isinstance(work_actor, str) and claimed_by.strip() != work_actor.strip():
            continue
        snapshot_ready = True
        break
    _record_session_start(moved, project_id, args.operation_id, snapshot_ready=snapshot_ready)
    return {"task": moved, "operation_id": args.operation_id}


def cmd_progress(client: Client, args: argparse.Namespace) -> Any:
    args.operation_id = _command_operation_id(getattr(args, "operation_id", None))
    current = _task(client, args.task)
    if _structured_progress_supplied(args):
        if not _active_claim(current):
            raise HelmError("structured progress requires an actively claimed task")
        payload = _structured_progress_payload(args)
        path = "/tasks/" + parse.quote(str(current["id"]), safe="") + "/progress"
        updated, _ = client.call(
            "POST",
            path,
            body=payload,
            if_match=current["version"],
            idempotency_key=_command_mutation_id(args.operation_id, "POST", path, payload),
        )
        if not isinstance(updated, dict) or not isinstance(updated.get("version"), int):
            raise HelmError("Helm returned an unexpected updated task")
        _record_session_progress(updated, current, payload)
        return {"task": updated, "operation_id": args.operation_id}
    message = _progress_message(args)
    path = "/tasks/" + parse.quote(str(current["id"]), safe="") + "/comments"
    comment_body = {"body": message}
    comment, _ = client.call(
        "POST",
        path,
        body=comment_body,
        idempotency_key=_command_mutation_id(args.operation_id, "POST", path, comment_body),
    )
    return {"task": current.get("key", current["id"]), "comment": comment}


def cmd_heartbeat(client: Client, args: argparse.Namespace) -> Any:
    """Refresh agent-work liveness without changing the task version.

    Heartbeats deliberately omit both If-Match and Idempotency-Key. The server
    operation is an inherently idempotent timestamp touch, and persisting an
    idempotency row for every hook pulse would create unnecessary write load.
    """

    args.operation_id = _validate_operation_id(args.operation_id)
    task_id = parse.quote(str(args.task), safe="")
    path = "/tasks/" + task_id + "/heartbeat"
    body = {"operation_id": args.operation_id}
    payload, _ = client.call("POST", path, body=body)
    current = payload if isinstance(payload, dict) else {"id": args.task}
    _record_session_heartbeat(current, args)
    return {"task": payload, "operation_id": args.operation_id}


def _structured_progress_supplied(args: argparse.Namespace) -> bool:
    """Return whether the caller selected the structured progress API."""

    for field in ("state", "phase", "completed", "total", "next_step"):
        if getattr(args, field, None) is not None:
            return True
    refs = getattr(args, "checkpoint_refs", ())
    return isinstance(refs, (list, tuple)) and bool(refs)


def _active_claim(task: dict[str, Any]) -> bool:
    """Check the claim data available in a task response before posting."""

    claimed_by = task.get("claimed_by")
    if not isinstance(claimed_by, str) or not claimed_by.strip():
        return False
    # Older task representations may expose the claimant without the lease
    # timestamp. Let the progress endpoint remain authoritative in that case,
    # while rejecting an explicitly absent, malformed, or expired lease.
    if "claim_expires_at" not in task:
        return True
    expires_at = task.get("claim_expires_at")
    if not isinstance(expires_at, str) or not expires_at.strip():
        return False
    try:
        expires = datetime.fromisoformat(expires_at.replace("Z", "+00:00"))
    except ValueError:
        return False
    if expires.tzinfo is None:
        expires = expires.replace(tzinfo=timezone.utc)
    return expires > datetime.now(timezone.utc)


def _structured_progress_payload(args: argparse.Namespace) -> dict[str, Any]:
    completed = getattr(args, "completed", None)
    total = getattr(args, "total", None)
    if (completed is None) != (total is None):
        raise HelmError("completed and total must be provided together")
    if completed is not None and (total < 1 or total > 100 or completed < 0 or completed > total):
        raise HelmError("progress must satisfy 0 <= completed <= total <= 100")

    operation_id = _validate_operation_id(getattr(args, "operation_id", ""))
    state = getattr(args, "state", None)
    message = getattr(args, "message", "")
    payload: dict[str, Any] = {
        "operation_id": operation_id.strip(),
        "state": state or "working",
        "summary": message.strip(),
    }
    phase = getattr(args, "phase", None)
    if phase is not None:
        phase = phase.strip()
        if not phase:
            raise HelmError("phase must not be empty")
        payload["phase"] = phase
    next_step = getattr(args, "next_step", None)
    if next_step is not None:
        next_step = next_step.strip()
        if not next_step:
            raise HelmError("next action must not be empty")
        payload["next_action"] = next_step
    if completed is not None:
        payload["checkpoint_completed"] = completed
        payload["checkpoint_total"] = total

    refs = getattr(args, "checkpoint_refs", ())
    if not isinstance(refs, (list, tuple)):
        refs = ()
    if refs:
        normalized_refs = [ref.strip() if isinstance(ref, str) else "" for ref in refs]
        if any(not isinstance(ref, str) or not ref for ref in normalized_refs):
            raise HelmError("checkpoint references must be non-empty")
        if len(normalized_refs) > 100:
            raise HelmError("checkpoint references must contain at most 100 items")
        if len(set(normalized_refs)) != len(normalized_refs):
            raise HelmError("checkpoint references must not contain duplicates")
        if any(len(ref) > 128 for ref in normalized_refs):
            raise HelmError("checkpoint references must be at most 128 characters")
        if completed is None:
            raise HelmError("checkpoint counts are required when checkpoint refs are provided")
        if len(normalized_refs) != total:
            raise HelmError("checkpoint refs must match checkpoint total")
        payload["checkpoint_refs"] = normalized_refs
    return payload


def _canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def _progress_message(args: argparse.Namespace) -> str:
    completed = args.completed
    total = args.total
    if (completed is None) != (total is None):
        raise HelmError("completed and total must be provided together")
    if completed is not None and (total < 1 or completed < 0 or completed > total):
        raise HelmError("progress must satisfy 0 <= completed <= total and total >= 1")
    if not any((args.state, args.phase, args.next_step, completed is not None)):
        return args.message.strip()
    heading = ["Agent update"]
    if args.state:
        heading.append(args.state.capitalize())
    if args.phase:
        heading.append(args.phase.strip())
    if completed is not None:
        heading.append(f"{completed}/{total}")
    lines = [" · ".join(heading), "", args.message.strip()]
    if args.next_step:
        lines.extend(("", "Next: " + args.next_step.strip()))
    return "\n".join(lines)


def _action(client: Client, args: argparse.Namespace, action: str, field: str) -> Any:
    args.operation_id = _command_operation_id(getattr(args, "operation_id", None))
    current = _task(client, args.task)
    value = getattr(args, field)
    path = "/tasks/" + parse.quote(str(current["id"]), safe="") + "/" + action
    body = {field: value.strip()}
    payload, _ = client.call(
        "POST",
        path,
        body=body,
        if_match=current["version"],
        idempotency_key=_command_mutation_id(args.operation_id, "POST", path, body),
    )
    _clear_matching_session(current, args.operation_id)
    return {"task": payload, "operation_id": args.operation_id}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    projects = subparsers.add_parser("projects", help="List visible projects")
    projects.add_argument("--limit", type=int, default=200)
    projects.add_argument("--cursor", default="")
    projects.add_argument("--all", action="store_true", help="Follow every cursor page")
    projects.set_defaults(handler=cmd_projects)

    tasks = subparsers.add_parser("tasks", help="List or search tasks in a project")
    tasks.add_argument("--project", required=True)
    tasks.add_argument("--query", default="")
    tasks.add_argument("--limit", type=int, default=200)
    tasks.add_argument("--cursor", default="")
    tasks.add_argument("--all", action="store_true", help="Follow every cursor page")
    tasks.set_defaults(handler=cmd_tasks)

    auth_check = subparsers.add_parser(
        "auth-check",
        aliases=("auth", "auth_check"),
        help="Validate the configured agent token without mutating Helm",
    )
    auth_check.set_defaults(handler=cmd_auth_check)

    events = subparsers.add_parser(
        "events", aliases=("event", "events-poll"), help="Poll events after a monotonic cursor"
    )
    events.add_argument("--after", type=int, default=0, help="Last event cursor observed (default: 0)")
    events.add_argument("--project", help="Restrict events to a project ID, key, or slug")
    events.add_argument("--limit", type=int, default=50)
    events.add_argument("--all", action="store_true", help="Follow every event page")
    events.set_defaults(handler=cmd_events)

    timeline = subparsers.add_parser(
        "timeline",
        aliases=("activity", "task-timeline", "project-timeline"),
        help="Read a task or project timeline",
    )
    timeline_target = timeline.add_mutually_exclusive_group(required=True)
    timeline_target.add_argument("--task", help="Task key or opaque task ID")
    timeline_target.add_argument("--project", help="Project key, slug, or opaque project ID")
    timeline.add_argument("--before", default="", help="Opaque cursor from a prior page")
    timeline.add_argument(
        "--kind", choices=("agent_progress", "comment", "task_change"), help="Optional activity kind filter"
    )
    timeline.add_argument("--limit", type=int, default=50)
    timeline.add_argument("--all", action="store_true", help="Follow every opaque cursor page")
    timeline.set_defaults(handler=cmd_timeline)

    renew = subparsers.add_parser("renew", help="Renew the current owner's active task lease")
    renew.add_argument("--task", required=True)
    renew.add_argument("--lease-seconds", type=int, choices=range(30, 604801), default=1800)
    renew.add_argument("--operation-id", help="UUIDv4 for deterministic replay (generated when omitted)")
    renew.set_defaults(handler=cmd_renew)

    release = subparsers.add_parser("release", help="Release the current owner's active task lease")
    release.add_argument("--task", required=True)
    release.add_argument("--operation-id", help="UUIDv4 for deterministic replay (generated when omitted)")
    release.set_defaults(handler=cmd_release)

    def add_dependency_args(parser: argparse.ArgumentParser, *, mutation: bool) -> None:
        parser.add_argument("--task", required=True, help="Dependent task key or opaque ID")
        if mutation:
            parser.add_argument(
                "--prerequisite",
                "--depends-on",
                required=True,
                help="Prerequisite task key or opaque ID",
            )
            parser.add_argument(
                "--operation-id",
                help="UUIDv4 for deterministic replay (generated when omitted)",
            )

    dependencies = subparsers.add_parser(
        "dependencies", aliases=("dependency",), help="List or mutate direct task dependencies"
    )
    dependency_actions = dependencies.add_subparsers(dest="dependency_action", required=True)
    dependency_list = dependency_actions.add_parser("list", help="List prerequisites and dependents")
    add_dependency_args(dependency_list, mutation=False)
    dependency_list.set_defaults(handler=cmd_dependency_list)
    dependency_add = dependency_actions.add_parser("add", help="Add a prerequisite edge")
    add_dependency_args(dependency_add, mutation=True)
    dependency_add.set_defaults(handler=cmd_dependency_add)
    dependency_remove = dependency_actions.add_parser("remove", help="Remove a prerequisite edge")
    add_dependency_args(dependency_remove, mutation=True)
    dependency_remove.set_defaults(handler=cmd_dependency_remove)

    for name, handler, mutation in (
        ("dependency-list", cmd_dependency_list, False),
        ("dependency-add", cmd_dependency_add, True),
        ("dependency-remove", cmd_dependency_remove, True),
    ):
        dependency_command = subparsers.add_parser(name, help=f"{name.replace('-', ' ').capitalize()}")
        add_dependency_args(dependency_command, mutation=mutation)
        dependency_command.set_defaults(handler=handler)

    def add_issue_filter_args(parser: argparse.ArgumentParser) -> None:
        parser.add_argument("--project")
        parser.add_argument("--state")
        parser.add_argument("--column")
        parser.add_argument("--priority")
        parser.add_argument("--severity")
        parser.add_argument("--label")
        parser.add_argument("--assignee")
        parser.add_argument("--reporter")
        parser.add_argument("--resolution")
        parser.add_argument("--agent-state")
        parser.add_argument("--action-needed", choices=("true", "false"))
        parser.add_argument("--query", "-q")
        parser.add_argument("--updated-after")
        parser.add_argument("--cursor", default="")
        parser.add_argument("--limit", type=int, default=50)
        parser.add_argument("--all", action="store_true", help="Follow every cursor page")

    issues = subparsers.add_parser(
        "issues",
        aliases=("issue", "bug-list", "issue-list"),
        help="List visible bug tasks with lifecycle filters",
    )
    add_issue_filter_args(issues)
    issues.set_defaults(handler=cmd_issues)

    def add_bug_report_args(parser: argparse.ArgumentParser) -> None:
        parser.add_argument("--project", required=True)
        parser.add_argument("--title", required=True)
        parser.add_argument("--actual-behavior", required=True, dest="actual_behavior")
        parser.add_argument("--expected-behavior", dest="expected_behavior")
        parser.add_argument("--reproduction-steps", dest="reproduction_steps")
        parser.add_argument("--environment")
        parser.add_argument("--affected-version", dest="affected_version")
        parser.add_argument("--severity", choices=("s1", "s2", "s3", "s4"))
        parser.add_argument("--description")
        parser.add_argument("--priority", choices=("low", "normal", "high", "urgent"))
        parser.add_argument("--column")
        parser.add_argument("--assignee")
        parser.add_argument("--operation-id", help="UUIDv4 for deterministic replay (generated when omitted)")

    bug_report = subparsers.add_parser(
        "bug-report", aliases=("report-bug", "report"), help="Create a structured bug task"
    )
    add_bug_report_args(bug_report)
    bug_report.set_defaults(handler=cmd_bug_report)

    def add_bug_action_args(parser: argparse.ArgumentParser, action: str) -> None:
        parser.add_argument("--task", required=True, help="Bug key or opaque task ID")
        parser.add_argument("--operation-id", help="UUIDv4 for deterministic replay (generated when omitted)")
        if action == "triage":
            parser.add_argument("--severity", required=True, choices=("s1", "s2", "s3", "s4"))
            parser.add_argument("--priority", choices=("low", "normal", "high", "urgent"))
            parser.add_argument("--assignee")
            parser.add_argument("--column")
        elif action == "resolve":
            parser.add_argument(
                "--resolution",
                required=True,
                choices=("fixed", "duplicate", "not_planned", "cannot_reproduce", "works_as_designed"),
            )
            parser.add_argument("--duplicate-of", dest="duplicate_of")
            parser.add_argument("--note")
        elif action == "duplicate":
            parser.add_argument("--duplicate-of", required=True, dest="duplicate_of")
            parser.add_argument("--note")
        elif action == "reopen":
            parser.add_argument("--reason", required=True)

    bug_action_specs = (
        ("bug-triage", cmd_bug_triage, "triage"),
        ("bug-resolve", cmd_bug_resolve, "resolve"),
        ("bug-duplicate", cmd_bug_duplicate, "duplicate"),
        ("bug-reopen", cmd_bug_reopen, "reopen"),
    )
    for name, handler, action in bug_action_specs:
        bug_action = subparsers.add_parser(name, help=f"{action.capitalize()} a bug")
        add_bug_action_args(bug_action, action)
        bug_action.set_defaults(handler=handler)

    bug = subparsers.add_parser("bug", aliases=("bugs",), help="Run a bug report, list, or lifecycle command")
    bug_actions = bug.add_subparsers(dest="bug_action", required=True)
    bug_list = bug_actions.add_parser("list", help="List visible bugs")
    add_issue_filter_args(bug_list)
    bug_list.set_defaults(handler=cmd_issues)
    bug_report_nested = bug_actions.add_parser("report", help="Create a structured bug")
    add_bug_report_args(bug_report_nested)
    bug_report_nested.set_defaults(handler=cmd_bug_report)
    for action, handler in (("triage", cmd_bug_triage), ("resolve", cmd_bug_resolve), ("duplicate", cmd_bug_duplicate), ("reopen", cmd_bug_reopen)):
        nested = bug_actions.add_parser(action, help=f"{action.capitalize()} a bug")
        add_bug_action_args(nested, action)
        nested.set_defaults(handler=handler)

    audit = subparsers.add_parser("audit", help="Read-only audit of board column placement")
    audit.add_argument("--project", required=True)
    audit.add_argument(
        "--state",
        "--semantic-state",
        dest="states",
        action="append",
        choices=AUDIT_SEMANTIC_STATES,
        help="Semantic state to inspect; repeat to select more than one (default: backlog and active)",
    )
    audit.add_argument("--limit", "--page-size", dest="page_size", type=int, default=200)
    audit.add_argument(
        "--evidence-limit",
        type=int,
        default=AUDIT_MAX_EVIDENCE_ITEMS,
        help=f"Maximum recent comments per task to include (0-{AUDIT_MAX_EVIDENCE_ITEMS})",
    )
    audit.set_defaults(handler=cmd_audit)

    submit = subparsers.add_parser(
        "submit",
        aliases=("audit-submit", "submit-audit"),
        help="Persist an audit context as a durable run and findings",
    )
    submit.add_argument("--project", required=True)
    submit.add_argument(
        "--audit",
        help="Append to and finalize an existing queued/running audit instead of creating a run",
    )
    submit.add_argument("--input", "--findings", "--findings-file", dest="input_path", required=True)
    submit.add_argument("--scope", default="board")
    submit.add_argument("--status", choices=("complete", "partial", "failed"), default="complete")
    submit.add_argument("--operation-id", required=True)
    submit.set_defaults(handler=cmd_submit)

    reconcile = subparsers.add_parser(
        "reconcile",
        help="Preview approved audit moves; use --apply to mutate explicitly",
    )
    reconcile.add_argument("--audit", required=True)
    reconcile.add_argument("--apply", action="store_true", help="Apply eligible approved moves")
    reconcile.add_argument("--limit", "--page-size", dest="page_size", type=int, default=200)
    reconcile.add_argument(
        "--operation-id",
        help="Stable idempotency namespace (default: audit-reconcile-<audit-id>)",
    )
    reconcile.set_defaults(handler=cmd_reconcile)

    start = subparsers.add_parser("start", help="Create and claim an active task")
    start.add_argument("--project", required=True)
    start.add_argument("--title", required=True)
    start.add_argument("--goal", required=True)
    start.add_argument("--step", action="append", default=[])
    start.add_argument("--priority", choices=("low", "normal", "high", "urgent"), default="normal")
    start.add_argument("--lease-seconds", type=int, choices=range(30, 604801), default=604800)
    start.add_argument("--operation-id", help="UUIDv4 for new work; explicit legacy IDs remain deterministic")
    start.set_defaults(handler=cmd_start)

    backlog = subparsers.add_parser("backlog", help="Create an unclaimed backlog task")
    backlog.add_argument("--project", required=True)
    backlog.add_argument("--title", required=True)
    backlog.add_argument("--goal", required=True)
    backlog.add_argument("--step", action="append", default=[])
    backlog.add_argument("--priority", choices=("low", "normal", "high", "urgent"), default="normal")
    backlog.add_argument("--operation-id", help="UUIDv4 for new work; explicit legacy IDs remain deterministic")
    backlog.set_defaults(handler=cmd_backlog)

    resume = subparsers.add_parser("resume", help="Claim or renew an existing task")
    resume.add_argument("--task", required=True)
    resume.add_argument("--lease-seconds", type=int, choices=range(30, 604801), default=604800)
    resume.add_argument("--operation-id", help="UUIDv4 for new work; explicit legacy IDs remain deterministic")
    resume.set_defaults(handler=cmd_resume)

    heartbeat = subparsers.add_parser("heartbeat", help="Refresh agent-work liveness")
    heartbeat.add_argument("--task", required=True)
    heartbeat.add_argument("--operation-id", required=True)
    heartbeat.set_defaults(handler=cmd_heartbeat)

    progress = subparsers.add_parser("progress", help="Post a meaningful progress comment")
    progress.add_argument("--task", required=True)
    progress.add_argument("--message", required=True)
    progress.add_argument("--state", choices=("working", "waiting", "verifying", "handoff"))
    progress.add_argument("--phase")
    progress.add_argument("--completed", type=int)
    progress.add_argument("--total", type=int)
    progress.add_argument("--next", dest="next_step")
    progress.add_argument("--checkpoint-ref", action="append", dest="checkpoint_refs", default=[])
    progress.add_argument("--operation-id", help="UUIDv4 for new work; explicit legacy IDs remain deterministic")
    progress.set_defaults(handler=cmd_progress)

    complete = subparsers.add_parser("complete", help="Complete a claimed task")
    complete.add_argument("--task", required=True)
    complete.add_argument("--comment", required=True)
    complete.add_argument("--operation-id", help="UUIDv4 for new work; explicit legacy IDs remain deterministic")
    complete.set_defaults(handler=lambda client, args: _action(client, args, "complete", "comment"))

    block = subparsers.add_parser("block", help="Block a claimed task")
    block.add_argument("--task", required=True)
    block.add_argument("--reason", required=True)
    block.add_argument("--operation-id", help="UUIDv4 for new work; explicit legacy IDs remain deterministic")
    block.set_defaults(handler=lambda client, args: _action(client, args, "block", "reason"))
    return parser


def main() -> int:
    try:
        args = build_parser().parse_args()
        result = args.handler(Client(load_config()), args)
        json.dump(result, sys.stdout, ensure_ascii=False, separators=(",", ":"))
        sys.stdout.write("\n")
        return (
            1
            if args.command in {"auth-check", "auth", "auth_check"}
            and isinstance(result, dict)
            and not result.get("ok", True)
            else 0
        )
    except HelmError as exc:
        payload = exc.as_dict()
        env_secrets = tuple(
            os.environ.get(name, "")
            for name in (
                "HELM_TOKEN",
                "TC_ROADMAP_TOKEN",
                "ROADMAP_TOKEN",
                "HELM_CF_ACCESS_CLIENT_ID",
                "HELM_CF_ACCESS_CLIENT_SECRET",
                "TC_CF_ACCESS_CLIENT_ID",
                "TC_CF_ACCESS_CLIENT_SECRET",
            )
        )
        for field in ("code", "message", "retry_after"):
            value = payload.get(field)
            if isinstance(value, str):
                payload[field] = _redact_secrets(value, env_secrets)[:300 if field == "message" else 120]
        json.dump({"error": payload}, sys.stderr, ensure_ascii=False, separators=(",", ":"))
        sys.stderr.write("\n")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
