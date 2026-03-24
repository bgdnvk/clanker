#!/usr/bin/env python3
"""
JSON Lines bridge between clanker Go CLI and Hermes AIAgent.

Protocol: JSON-RPC 2.0 over stdin (requests) / stdout (responses + notifications).
All diagnostic output goes to stderr so stdout stays clean for the protocol.

Supported methods:
  initialize  - returns bridge version
  prompt      - runs a user prompt through the configured backend

Supported backends (via HERMES_PROVIDER env):
  bedrock   - AWS Bedrock via boto3 (default if AWS_PROFILE is set)
  anthropic - Anthropic API directly
  openai    - OpenAI-compatible APIs (OpenRouter, etc.)
  hermes    - Full Hermes AIAgent (requires hermes-agent installed)
"""

import json
import logging
import os
import sys
import uuid

logging.basicConfig(
    stream=sys.stderr,
    level=logging.DEBUG if os.environ.get("HERMES_BRIDGE_DEBUG") else logging.INFO,
    format="[hermes-bridge] %(levelname)s %(message)s",
)
log = logging.getLogger("hermes-bridge")

BRIDGE_VERSION = "0.2.0"

# Conversation history for multi-turn sessions.
_conversation = []


def send(obj):
    """Write a JSON object as a single line to stdout."""
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def send_notification(method, params):
    """Send a JSON-RPC notification (no id)."""
    send({"jsonrpc": "2.0", "method": method, "params": params})


def send_result(req_id, result):
    """Send a JSON-RPC success response."""
    send({"jsonrpc": "2.0", "result": result, "id": req_id})


def send_error(req_id, code, message):
    """Send a JSON-RPC error response."""
    send({"jsonrpc": "2.0", "error": {"code": code, "message": message}, "id": req_id})


def recv():
    """Read a single JSON-RPC request from stdin. Returns None on EOF."""
    line = sys.stdin.readline()
    if not line:
        return None
    line = line.strip()
    if not line:
        return None
    try:
        return json.loads(line)
    except json.JSONDecodeError as exc:
        log.error("malformed JSON on stdin: %s", exc)
        return None


def _detect_provider():
    """Pick the best available provider based on environment."""
    explicit = os.environ.get("HERMES_PROVIDER", "").strip().lower()
    if explicit:
        return explicit

    # If AWS profile is set and no explicit API keys, default to bedrock.
    if os.environ.get("AWS_PROFILE") or os.environ.get("HERMES_BEDROCK_MODEL"):
        return "bedrock"

    if os.environ.get("ANTHROPIC_API_KEY"):
        return "anthropic"

    if os.environ.get("OPENAI_API_KEY") or os.environ.get("OPENROUTER_API_KEY"):
        return "openai"

    return "bedrock"


def _prompt_bedrock(text, req_id, session_id):
    """Call AWS Bedrock Converse API via boto3."""
    import boto3

    profile = os.environ.get("AWS_PROFILE", "")
    region = os.environ.get("AWS_REGION", os.environ.get("AWS_DEFAULT_REGION", "us-east-1"))
    model_id = os.environ.get("HERMES_BEDROCK_MODEL", "anthropic.claude-3-haiku-20240307-v1:0")

    log.info("bedrock: profile=%s region=%s model=%s", profile, region, model_id)

    session = boto3.Session(profile_name=profile if profile else None, region_name=region)
    client = session.client("bedrock-runtime", region_name=region)

    # Add user message to conversation history.
    _conversation.append({"role": "user", "content": [{"text": text}]})

    try:
        response = client.converse(
            modelId=model_id,
            messages=_conversation,
            inferenceConfig={"maxTokens": 4096, "temperature": 0.7},
        )
    except Exception as exc:
        _conversation.pop()  # Remove the failed user message
        raise exc

    # Extract the assistant response.
    output = response.get("output", {})
    message = output.get("message", {})
    content_blocks = message.get("content", [])

    parts = []
    for block in content_blocks:
        if "text" in block:
            chunk = block["text"]
            parts.append(chunk)
            send_notification("message_delta", {"text": chunk, "done": False})

    full_text = "".join(parts)

    # Add assistant response to conversation history.
    _conversation.append({"role": "assistant", "content": [{"text": full_text}]})

    send_result(req_id, {"text": full_text, "session_id": session_id})


def _prompt_anthropic(text, req_id, session_id):
    """Call Anthropic API directly."""
    import anthropic

    api_key = os.environ.get("ANTHROPIC_API_KEY", "")
    model = os.environ.get("HERMES_MODEL", "claude-sonnet-4-20250514")

    log.info("anthropic: model=%s", model)

    client = anthropic.Anthropic(api_key=api_key)

    _conversation.append({"role": "user", "content": text})

    response = client.messages.create(
        model=model,
        max_tokens=4096,
        messages=_conversation,
    )

    full_text = ""
    for block in response.content:
        if block.type == "text":
            full_text += block.text
            send_notification("message_delta", {"text": block.text, "done": False})

    _conversation.append({"role": "assistant", "content": full_text})

    send_result(req_id, {"text": full_text, "session_id": session_id})


def _prompt_openai(text, req_id, session_id):
    """Call OpenAI-compatible API (works with OpenRouter too)."""
    import openai

    base_url = os.environ.get("HERMES_BASE_URL", "https://openrouter.ai/api/v1")
    api_key = (
        os.environ.get("OPENROUTER_API_KEY")
        or os.environ.get("OPENAI_API_KEY")
        or ""
    )
    model = os.environ.get("HERMES_MODEL", "anthropic/claude-opus-4")

    log.info("openai-compat: model=%s base_url=%s", model, base_url)

    client = openai.OpenAI(api_key=api_key, base_url=base_url)

    _conversation.append({"role": "user", "content": text})

    response = client.chat.completions.create(
        model=model,
        messages=_conversation,
        max_tokens=4096,
    )

    full_text = response.choices[0].message.content or ""
    send_notification("message_delta", {"text": full_text, "done": False})

    _conversation.append({"role": "assistant", "content": full_text})

    send_result(req_id, {"text": full_text, "session_id": session_id})


def _prompt_hermes(text, req_id, session_id):
    """Use the full Hermes AIAgent."""
    try:
        from run_agent import AIAgent
    except ImportError:
        raise ImportError(
            "Cannot import AIAgent. Ensure hermes-agent is installed "
            "and this script is run from the hermes venv."
        )

    base_url = os.environ.get("HERMES_BASE_URL", "https://openrouter.ai/api/v1")
    model = os.environ.get("HERMES_MODEL", "anthropic/claude-opus-4")
    api_key = (
        os.environ.get("OPENROUTER_API_KEY")
        or os.environ.get("OPENAI_API_KEY")
        or os.environ.get("ANTHROPIC_API_KEY")
        or ""
    )

    log.info("hermes agent: model=%s base_url=%s", model, base_url)

    agent = AIAgent(base_url=base_url, model=model, api_key=api_key)

    collected = []

    def on_message(chunk):
        collected.append(chunk)
        send_notification("message_delta", {"text": chunk, "done": False})

    def on_tool(name, preview, args):
        send_notification("tool_call", {"name": name, "args": json.dumps(args) if args else ""})

    def on_thinking(chunk):
        send_notification("thought", {"text": chunk})

    result = agent.run_conversation(
        text,
        message_callback=on_message,
        tool_progress_callback=on_tool,
        thinking_callback=on_thinking,
    )

    if result is None:
        result = "".join(collected)

    send_result(req_id, {"text": result, "session_id": session_id})


PROVIDERS = {
    "bedrock": _prompt_bedrock,
    "anthropic": _prompt_anthropic,
    "openai": _prompt_openai,
    "hermes": _prompt_hermes,
}


def handle_initialize(req):
    """Handle the 'initialize' method."""
    provider = _detect_provider()
    send_result(req["id"], {"version": BRIDGE_VERSION, "provider": provider})


def handle_prompt(req):
    """Handle the 'prompt' method."""
    params = req.get("params", {})
    text = params.get("text", "")
    session_id = params.get("session_id", str(uuid.uuid4()))
    req_id = req["id"]

    if not text.strip():
        send_error(req_id, -32602, "empty prompt text")
        return

    provider = _detect_provider()
    handler = PROVIDERS.get(provider)
    if handler is None:
        send_error(req_id, -32000, f"unknown provider: {provider}")
        return

    log.info("prompt [%s]: %s", provider, text[:80])

    try:
        handler(text, req_id, session_id)
    except Exception as exc:
        log.exception("provider error (%s)", provider)
        send_error(req_id, -32000, f"{provider} error: {exc}")


def main():
    """Main JSON Lines request loop."""
    log.info("bridge starting (version %s)", BRIDGE_VERSION)

    handlers = {
        "initialize": handle_initialize,
        "prompt": handle_prompt,
    }

    while True:
        req = recv()
        if req is None:
            log.info("stdin closed, exiting")
            break

        method = req.get("method", "")
        handler = handlers.get(method)
        if handler is None:
            send_error(
                req.get("id", 0),
                -32601,
                f"unknown method: {method}",
            )
            continue

        try:
            handler(req)
        except Exception as exc:
            log.exception("unhandled error in handler for %s", method)
            send_error(req.get("id", 0), -32603, str(exc))


if __name__ == "__main__":
    main()
