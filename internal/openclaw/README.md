# OpenClaw Package

## TODO: Switch from `dangerouslyAllowHostHeaderOriginFallback` to `allowedOrigins`

Currently we use the break-glass flag `gateway.controlUi.dangerouslyAllowHostHeaderOriginFallback: true`
in `openclaw.json` to bypass origin checks on non-loopback binds. This works but is the "dangerous" path
per OpenClaw docs.

### Recommended approach (per official docs + `docker-setup.sh`)

Set `gateway.controlUi.allowedOrigins` with the actual CloudFront domain instead:

```json
{
    "gateway": {
        "mode": "local",
        "controlUi": {
            "allowedOrigins": ["https://d1234abcdef.cloudfront.net"]
        }
    }
}
```

### Implementation plan

1. Keep the fallback flag during initial deploy (we don't know the CloudFront URL yet)
2. In `postdeploy_autofix.go`, after confirming the instance is healthy, patch `openclaw.json`
   via SSM to replace the fallback flag with the actual CloudFront `allowedOrigins`:
    ```
    docker run --rm -v openclaw_data:/data alpine:3.20 sh -c \
      'cat > /data/openclaw.json <<EOF
    {"gateway":{"mode":"local","controlUi":{"allowedOrigins":["https://<CF_DOMAIN>"]}}}
    EOF'
    ```
3. Restart the container so the config change takes effect (gateway.\* changes require restart)
4. Remove the fallback flag from `exec.go` and `openclaw.go` SSMRestartCommands once this is stable

### Where the CloudFront domain lives

- It's in the deploy plan output or can be fetched via `aws cloudfront list-distributions`
- The `resourcesGlue` step already knows about CloudFront resources
- Could also parse it from the plan's CloudFront command

### References

- Config ref: https://docs.openclaw.ai/gateway/configuration-reference (gateway.controlUi section)
- Docker setup script: https://github.com/openclaw/openclaw/blob/main/docker-setup.sh
  (see `ensure_control_ui_allowed_origins` function)
- Hetzner VPS guide: https://docs.openclaw.ai/install/hetzner
