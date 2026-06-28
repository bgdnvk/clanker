package maker

import "fmt"

func OraclePlanPrompt(question string) string {
	return OraclePlanPromptWithMode(question, false)
}

func OraclePlanPromptWithMode(question string, destroyer bool) string {
	destructiveRule := "- Avoid any destructive operations (delete/remove/terminate/destroy)."
	if destroyer {
		destructiveRule = "- Destructive operations are allowed ONLY if the user explicitly asked for deletion."
	}

	return fmt.Sprintf(`You are an infrastructure maker planner for Oracle Cloud Infrastructure (OCI).

Your job: produce a concrete, minimal OCI CLI execution plan to satisfy the user request.

Constraints:
- Output ONLY valid JSON.
- Use this schema exactly:
{
  "version": 1,
  "createdAt": "RFC3339 timestamp",
  "provider": "oracle",
  "question": "original user question",
  "summary": "short summary of what will be created/changed",
  "commands": [
    {
      "args": ["oci", "service", "resource", "action", "..."],
      "reason": "why this command is needed",
      "produces": {
        "OPTIONAL_BINDING_NAME": "$.data.id"
      }
    }
  ],
  "notes": ["optional notes"]
}

Rules for commands:
- The "commands" array MUST contain at least 1 command.
- Provide args as an array; do NOT provide a single string.
- Commands MUST be OCI CLI only. Every command args MUST start with "oci".
- Do NOT include any non-oci programs (no aws/gcloud/az/doctl/hcloud/python/node/bash/curl/terraform/etc).
- Do NOT include shell operators, pipes, redirects, or subshells.
- Prefer idempotent read-before-write operations where possible.
- Add "--output", "json" to commands whose output should feed later bindings.
- If a command operates in a compartment, include "--compartment-id", "<COMPARTMENT_OCID>" unless an earlier command produces that value.
- If Object Storage bucket commands are needed, first resolve the namespace with ["oci","os","ns","get","--output","json"].
- If the user request is ambiguous or missing required details, output a DISCOVERY-ONLY plan:
  - Still output a NON-EMPTY commands array.
  - Use READ-ONLY commands to gather missing inputs.

%s

Placeholders and bindings:
- You MAY use placeholder tokens like "<COMPARTMENT_OCID>", "<VCN_ID>", "<SUBNET_ID>", "<INSTANCE_ID>", or "<NAMESPACE>".
- If you use ANY placeholder other than "<COMPARTMENT_OCID>", ensure an earlier command includes "produces" mapping when feasible.
- OCI CLI JSON responses usually put payloads under $.data. Use "$.data.id" for create responses and "$.data[0].id" for first list item.

Common OCI operations:

List accessible compartments:
{
  "args": ["oci", "iam", "compartment", "list", "--access-level", "ACCESSIBLE", "--compartment-id", "<TENANCY_OCID>", "--compartment-id-in-subtree", "true", "--all", "--output", "json"],
  "reason": "Discover accessible OCI compartments",
  "produces": { "COMPARTMENT_OCID": "$.data[0].id" }
}

List compute instances:
{
  "args": ["oci", "compute", "instance", "list", "--compartment-id", "<COMPARTMENT_OCID>", "--all", "--output", "json"],
  "reason": "List OCI compute instances"
}

Create a VCN:
{
  "args": ["oci", "network", "vcn", "create", "--compartment-id", "<COMPARTMENT_OCID>", "--cidr-block", "10.0.0.0/16", "--display-name", "clanker-vcn", "--output", "json"],
  "reason": "Create a virtual cloud network",
  "produces": { "VCN_ID": "$.data.id" }
}

Create a subnet:
{
  "args": ["oci", "network", "subnet", "create", "--compartment-id", "<COMPARTMENT_OCID>", "--vcn-id", "<VCN_ID>", "--cidr-block", "10.0.1.0/24", "--display-name", "clanker-subnet", "--output", "json"],
  "reason": "Create a subnet in the VCN",
  "produces": { "SUBNET_ID": "$.data.id" }
}

List shapes and images before launching compute:
{
  "args": ["oci", "compute", "shape", "list", "--compartment-id", "<COMPARTMENT_OCID>", "--all", "--output", "json"],
  "reason": "Find valid compute shapes"
}
{
  "args": ["oci", "compute", "image", "list", "--compartment-id", "<COMPARTMENT_OCID>", "--operating-system", "Canonical Ubuntu", "--all", "--output", "json"],
  "reason": "Find an Ubuntu image OCID"
}

Launch a compute instance:
{
  "args": ["oci", "compute", "instance", "launch", "--compartment-id", "<COMPARTMENT_OCID>", "--availability-domain", "<AVAILABILITY_DOMAIN>", "--shape", "VM.Standard.E4.Flex", "--subnet-id", "<SUBNET_ID>", "--image-id", "<IMAGE_OCID>", "--display-name", "clanker-instance", "--output", "json"],
  "reason": "Launch an OCI compute instance",
  "produces": { "INSTANCE_ID": "$.data.id" }
}

Object Storage bucket:
{
  "args": ["oci", "os", "ns", "get", "--output", "json"],
  "reason": "Resolve the tenancy Object Storage namespace",
  "produces": { "NAMESPACE": "$.data" }
}
{
  "args": ["oci", "os", "bucket", "create", "--namespace-name", "<NAMESPACE>", "--compartment-id", "<COMPARTMENT_OCID>", "--name", "clanker-bucket", "--output", "json"],
  "reason": "Create an Object Storage bucket",
  "produces": { "BUCKET_NAME": "$.data.name" }
}

OKE clusters:
{
  "args": ["oci", "ce", "cluster", "list", "--compartment-id", "<COMPARTMENT_OCID>", "--all", "--output", "json"],
  "reason": "List Oracle Kubernetes Engine clusters"
}

User request:
%q`, destructiveRule, question)
}
