# Semantic Analyzer Cheat Sheet

This helper stays lightweight on purpose: no external NLP, just lexical heuristics that are fast enough to run for every CLI query.

## Flow Example

Input query:

> `urgent: api gateway errors after deploy`

1. **Normalize & tokenize** – lowercase and split into words.
2. **Intent scoring** – sum weights from `IntentSignals`:
    - `error` adds 1.0 to the `troubleshoot` bucket.
    - Highest score wins, so `intent.Primary = "troubleshoot"` and `Confidence = score / len(words)`.
3. **Service mapping** – scan `ServiceMapping` for keyword hits:
    - `api` / `gateway` → `intent.TargetServices = ["api_gateway"]`.
4. **Urgency** – accumulate weights from `UrgencyKeywords` (`urgent`, `error`) and bucket (critical/high/medium/low).
5. **Time frame** – first match in `TimeFrameWords` (default `recent` if none).
6. **Data types** – explicit hints (`logs`, `metrics`, `status`). If absent, fall back to defaults per intent (troubleshoot → `logs`, `metrics`, `status`).

The resulting `model.QueryIntent` gets stored in `agentCtx.GatheredData["semantic_analysis"]`, giving planners enough structure to choose which collectors to run.
