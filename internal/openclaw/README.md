# OpenClaw Package

## Origin allowlist approach (`allowedOrigins`)

We use `gateway.controlUi.allowedOrigins` in `openclaw.json` per OpenClaw docs.
This is the recommended way (same as `docker-setup.sh`'s `ensure_control_ui_allowed_origins`).

### How it works

1. **Initial boot** (`exec.go` user-data): Container starts with `allowedOrigins: ["http://127.0.0.1:<port>"]`.
   CloudFront doesn't exist yet, but localhost is enough for health checks and SSM port-forward access.
2. **Post-deploy** (`exec.go` after `MaybeEnsureHTTPSViaCloudFront`): Once the CloudFront domain is known,
   SSM patches `openclaw.json` to add `"https://<cf-domain>"` and restarts the container.
3. **SSM restart / re-bootstrap** (`openclaw.go`, `postdeploy_autofix.go`): Uses `ConfigWriteShellCmd(cfDomain, port)`
   which reads `CLOUDFRONT_DOMAIN` from bindings and includes it if available.

### Config written

```json
{
    "gateway": {
        "mode": "local",
        "controlUi": {
            "allowedOrigins": [
                "http://127.0.0.1:18789",
                "https://d1234abcdef.cloudfront.net"
            ]
        }
    }
}
```

### Key functions

- `ConfigJSON(cfDomain, port)` — returns the JSON config string
- `ConfigWriteShellCmd(cfDomain, port)` — returns the shell command to write it via alpine init container

### Legacy cleanup

The `--dangerously-allow-host-header-origin-fallback` CLI flag is stripped from start commands
if found (backward compat with old plans).

### References

- Config ref: https://docs.openclaw.ai/gateway/configuration-reference (`gateway.controlUi` section)
- Docker setup script: https://github.com/openclaw/openclaw/blob/main/docker-setup.sh
  (see `ensure_control_ui_allowed_origins` function)
- Hetzner VPS guide: https://docs.openclaw.ai/install/hetzner
