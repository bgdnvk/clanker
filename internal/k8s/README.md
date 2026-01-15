# K8s notes (kubeconfig + permissions gotchas)

This folder implements clanker’s Kubernetes support (`clanker k8s ...`).

## Common gotchas

### 1) Wrong `clanker` binary (Homebrew vs local build)

If you have a Homebrew-installed `clanker`, it may be earlier in your `PATH` than `/usr/local/bin`.
Symptoms: `clanker k8s ...` shows `unknown command "k8s"`.
Fix: ensure the installed binary is the one you intend to run.

### 2) Stale or missing kubeconfig / wrong context

Kubernetes access is driven by **kubeconfig** (usually `~/.kube/config`) and a selected **context**.

Symptoms:

-   Empty resource output (older behavior) or connection errors
-   DNS failures to old EKS endpoints (endpoint changed / kubeconfig stale)
-   Wrong cluster data because the current context points elsewhere

What clanker does:

-   When you pass `--cluster <name>`, clanker tries to select a kube context matching that cluster name (exact match or substring match).
-   If the kube context is missing OR the API connection fails, clanker will run `aws eks update-kubeconfig` (using the configured AWS profile/region) and retry.

### 3) EKS auth: “the server has asked for the client to provide credentials”

This means you can reach the API endpoint, but Kubernetes is not accepting your identity.

In EKS, Kubernetes authentication is IAM-backed:

-   You obtain a short-lived token via `aws eks get-token` (usually via kubeconfig `exec`).
-   The cluster must be configured to **grant access** to that IAM principal.

Common causes:

-   Your IAM user/role is not granted access to the EKS cluster.
    -   Newer EKS: missing **EKS Access Entry**.
    -   Older EKS: not mapped in the `aws-auth` ConfigMap.

What clanker does:

-   When kubectl fails with auth/RBAC-like errors, clanker prints a targeted hint including the **AWS identity ARN** (`aws sts get-caller-identity`) that it is using.

### 4) RBAC authz: “forbidden”

This means you’re authenticated, but not authorized to do the requested operation.

In Kubernetes RBAC terms:

-   You need a `RoleBinding` or `ClusterRoleBinding` that grants the required verbs/resources.

What clanker does:

-   Detects common RBAC failure strings and explains that you likely need a binding.

## What’s implemented to make this less painful

-   Automatic context selection for `--cluster <name>`.
-   Automatic `aws eks update-kubeconfig` refresh + retry when:
    -   kubeconfig/context is missing
    -   kubeconfig is stale (EKS endpoint rotation)
    -   initial API connection fails
-   Helpful error messages for EKS auth/RBAC failures including the AWS identity ARN.

## Troubleshooting commands (manual)

-   Show AWS identity:

    -   `aws sts get-caller-identity --profile <profile>`

-   Verify kube context exists:

    -   `kubectl config get-contexts -o name`

-   Verify cluster auth quickly:
    -   `kubectl --context <context> cluster-info`
