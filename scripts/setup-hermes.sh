#!/usr/bin/env bash
set -euo pipefail

# Setup script for Hermes Agent integration with clanker-cli.
# Clones hermes-agent into vendor/, creates a Python 3.11 venv,
# and installs all dependencies.

HERMES_REPO="https://github.com/NousResearch/hermes-agent.git"

# Find the project root by locating go.mod
find_project_root() {
    local dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"
    if [ -f "$dir/go.mod" ]; then
        echo "$dir"
        return
    fi
    # fallback: current directory
    if [ -f "go.mod" ]; then
        pwd
        return
    fi
    echo "Error: cannot find project root (no go.mod found)" >&2
    exit 1
}

PROJECT_ROOT="$(find_project_root)"
VENDOR_DIR="$PROJECT_ROOT/vendor/hermes-agent"

echo "[hermes-setup] project root: $PROJECT_ROOT"
echo "[hermes-setup] vendor dir:   $VENDOR_DIR"

# Check for uv
if ! command -v uv &>/dev/null; then
    echo "[hermes-setup] Error: 'uv' is not installed."
    echo "  Install it with: curl -LsSf https://astral.sh/uv/install.sh | sh"
    exit 1
fi

# Check for Python 3.11+
PYTHON_BIN=""
for candidate in python3.11 python3.12 python3.13 python3; do
    if command -v "$candidate" &>/dev/null; then
        version=$("$candidate" -c "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')" 2>/dev/null || true)
        major=$(echo "$version" | cut -d. -f1)
        minor=$(echo "$version" | cut -d. -f2)
        if [ "${major:-0}" -ge 3 ] && [ "${minor:-0}" -ge 11 ]; then
            PYTHON_BIN="$candidate"
            break
        fi
    fi
done

if [ -z "$PYTHON_BIN" ]; then
    echo "[hermes-setup] Error: Python 3.11+ is required but not found."
    echo "  Install Python 3.11 and ensure it is on your PATH."
    exit 1
fi

echo "[hermes-setup] using python: $PYTHON_BIN ($(${PYTHON_BIN} --version 2>&1))"

# Clone or update hermes-agent
if [ -d "$VENDOR_DIR/.git" ]; then
    echo "[hermes-setup] hermes-agent already cloned, pulling latest..."
    cd "$VENDOR_DIR"
    git pull --recurse-submodules
    git submodule update --init --recursive
else
    echo "[hermes-setup] cloning hermes-agent..."
    mkdir -p "$(dirname "$VENDOR_DIR")"
    git clone --recurse-submodules "$HERMES_REPO" "$VENDOR_DIR"
fi

cd "$VENDOR_DIR"

# Create Python virtual environment
echo "[hermes-setup] creating Python venv..."
uv venv --python "$PYTHON_BIN" .venv

# Activate venv for pip installs
VENV_PYTHON="$VENDOR_DIR/.venv/bin/python"

# Install hermes-agent with all extras
echo "[hermes-setup] installing hermes-agent[all]..."
uv pip install --python "$VENV_PYTHON" -e ".[all]"

# Install mini-swe-agent submodule if present
if [ -d "$VENDOR_DIR/mini-swe-agent" ] && [ -f "$VENDOR_DIR/mini-swe-agent/pyproject.toml" ]; then
    echo "[hermes-setup] installing mini-swe-agent..."
    uv pip install --python "$VENV_PYTHON" -e "./mini-swe-agent"
else
    echo "[hermes-setup] mini-swe-agent submodule not found, skipping"
fi

# Create ~/.hermes directory structure
HERMES_HOME="${HERMES_HOME:-$HOME/.hermes}"
mkdir -p "$HERMES_HOME"
echo "[hermes-setup] hermes home: $HERMES_HOME"

# Create template .env if it does not exist
if [ ! -f "$HERMES_HOME/.env" ]; then
    cat > "$HERMES_HOME/.env.template" <<'ENVTEMPLATE'
# Hermes Agent environment configuration.
# Copy this file to .env and fill in the keys you need.

# OpenRouter (recommended, access to 200+ models)
# OPENROUTER_API_KEY=

# OpenAI
# OPENAI_API_KEY=

# Anthropic
# ANTHROPIC_API_KEY=

# Google Gemini
# GEMINI_API_KEY=
ENVTEMPLATE
    echo "[hermes-setup] created $HERMES_HOME/.env.template"
    echo "  Copy it to $HERMES_HOME/.env and fill in your API keys."
else
    echo "[hermes-setup] $HERMES_HOME/.env already exists, not overwriting"
fi

# Smoke test
echo "[hermes-setup] running smoke test..."
if "$VENV_PYTHON" -c "from run_agent import AIAgent; print('[hermes-setup] smoke test passed: AIAgent imported')" 2>/dev/null; then
    :
else
    echo "[hermes-setup] warning: could not import AIAgent from run_agent"
    echo "  The agent may still work via hermes_cli imports."
    "$VENV_PYTHON" -c "import hermes_cli; print('[hermes-setup] smoke test passed: hermes_cli imported')" 2>/dev/null || \
        echo "[hermes-setup] warning: hermes_cli import also failed; check installation"
fi

echo ""
echo "[hermes-setup] setup complete!"
echo "  Hermes path: $VENDOR_DIR"
echo "  Python venv: $VENDOR_DIR/.venv"
echo ""
echo "  Add to your ~/.clanker.yaml:"
echo "    hermes:"
echo "      path: \"$VENDOR_DIR\""
echo "      model: \"anthropic/claude-opus-4\""
echo ""
echo "  Then use: clanker ask --agent hermes \"your question\""
echo "       or:  clanker talk"
