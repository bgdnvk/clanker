#!/usr/bin/env python3
"""
JSON Lines bridge between clanker Go CLI and Hermes AIAgent.

Protocol: JSON-RPC 2.0 over stdin (requests) / stdout (responses + notifications).
All diagnostic output goes to stderr so stdout stays clean for the protocol.

Supported methods:
  initialize  - returns bridge version
  prompt      - runs a user prompt through AIAgent and streams events
"""

import json
import logging
import os
import sys
import uuid
import threading

logging.basicConfig(
    stream=sys.stderr,
    level=logging.DEBUG if os.environ.get("HERMES_BRIDGE_DEBUG") else logging.INFO,
    format="[hermes-bridge] %(levelname)s %(message)s",
)
log = logging.getLogger("hermes-bridge")

# Global agent instance, reused across prompts for multi-turn sessions.
_agent = None
_agent_lock = threading.Lock()

BRIDGE_VERSION = "0.1.0"


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


def _get_or_create_agent():
    """Lazily create the Hermes AIAgent singleton."""
    global _agent
    with _agent_lock:
        if _agent is not None:
            return _agent

        # Determine API endpoint and model from environment.
        base_url = os.environ.get(
            "HERMES_BASE_URL", "https://openrouter.ai/api/v1"
        )
        model = os.environ.get("HERMES_MODEL", "anthropic/claude-opus-4")

        # Resolve API key: prefer explicit OPENROUTER, then OPENAI, then ANTHROPIC.
        api_key = (
            os.environ.get("OPENROUTER_API_KEY")
            or os.environ.get("OPENAI_API_KEY")
            or os.environ.get("ANTHROPIC_API_KEY")
            or ""
        )

        log.info("creating AIAgent: model=%s base_url=%s", model, base_url)

        # Try importing from the hermes-agent package.
        try:
            from run_agent import AIAgent
        except ImportError:
            try:
                from hermes.run_agent import AIAgent
            except ImportError:
                raise ImportError(
                    "Cannot import AIAgent. Ensure hermes-agent is installed "
                    "and this script is run from the hermes venv."
                )

        _agent = AIAgent(
            base_url=base_url,
            model=model,
            api_key=api_key,
        )
        return _agent


def handle_initialize(req):
    """Handle the 'initialize' method."""
    send_result(req["id"], {"version": BRIDGE_VERSION})


def handle_prompt(req):
    """Handle the 'prompt' method by running the AIAgent."""
    params = req.get("params", {})
    text = params.get("text", "")
    session_id = params.get("session_id", str(uuid.uuid4()))
    req_id = req["id"]

    if not text.strip():
        send_error(req_id, -32602, "empty prompt text")
        return

    try:
        agent = _get_or_create_agent()
    except ImportError as exc:
        send_error(req_id, -32000, str(exc))
        return
    except Exception as exc:
        send_error(req_id, -32000, f"failed to create agent: {exc}")
        return

    collected_text = []

    # Callbacks that map AIAgent events to JSON-RPC notifications.
    def on_message(text_chunk):
        collected_text.append(text_chunk)
        send_notification("message_delta", {"text": text_chunk, "done": False})

    def on_tool_progress(name, preview, args):
        send_notification("tool_call", {"name": name, "args": json.dumps(args) if args else ""})

    def on_thinking(text_chunk):
        send_notification("thought", {"text": text_chunk})

    try:
        # Run the agent. The AIAgent.run_conversation method is synchronous
        # and invokes callbacks during execution.
        response_text = agent.run_conversation(
            text,
            message_callback=on_message,
            tool_progress_callback=on_tool_progress,
            thinking_callback=on_thinking,
        )

        # If callbacks streamed the text, the final result may be redundant
        # but we include it for completeness.
        if response_text is None:
            response_text = "".join(collected_text)

        send_result(req_id, {"text": response_text, "session_id": session_id})

    except Exception as exc:
        log.exception("agent error")
        send_error(req_id, -32000, str(exc))


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
