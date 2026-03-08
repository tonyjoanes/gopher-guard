# Troubleshooting

## AegisWatch stuck in "Degraded" phase

**Symptom**: Phase stays `Degraded` and no PR is created.

Check operator logs:
```bash
kubectl logs -n gopher-guard-system -l app.kubernetes.io/name=gopher-guard -f
```

Common causes:
1. **Missing GitHub token**: Check that `gitSecretRef` exists and has a `token` key.
2. **LLM error**: Check for `DiagnosisFailed` events — `kubectl describe aegiswatch <name>`.
3. **MaxHealingAttempts reached**: `healingScore >= 5`. Reset by editing `status.healingScore` or recreating the CR.
4. **YAML patch produced no change**: The LLM patch was a no-op. Manual fix required.

## No PR created but diagnosis logged

- Check `spec.safeMode: true` — set to `false` to enable PR creation.
- Check `spec.gitRepo` format is exactly `"owner/repo"`.
- Check the deployment manifest exists at a [conventional path](configuration.md#deployment-manifest-discovery).

## LLM returns empty response

- Verify the API key is correct: `kubectl get secret <llmSecretRef> -o yaml`
- For Groq: check usage limits at groq.com
- For Ollama: ensure the model is pulled (`ollama pull llama3`) and the server is reachable

## Pod not triggering reconciliation

GopherGuard watches Pod changes and reconciles every 30 seconds. If a pod crashes and is not detected:
- Verify the `targetRef.name` matches the exact Deployment name
- Verify the `targetRef.namespace` is correct
- Check `restartThreshold` — default is 3, so the pod needs to restart at least 3 times

## Operator not starting

Check for RBAC errors:
```bash
kubectl describe pod -n gopher-guard-system -l app.kubernetes.io/name=gopher-guard
```

Ensure the CRD is installed:
```bash
kubectl get crd aegiswatches.ops.gopherguard.dev
```

## Metrics not appearing

Ensure Prometheus can scrape the operator:
```bash
kubectl port-forward -n gopher-guard-system svc/gopher-guard-metrics 8443:8443
curl -k https://localhost:8443/metrics | grep gopherguard
```

Available metrics:
- `gopherguard_reconcile_duration_seconds` — reconciliation latency
- `gopherguard_anomalies_detected_total` — anomalies by namespace and reason
- `gopherguard_healing_attempts_total` — PR creation attempts by outcome
- `gopherguard_llm_request_duration_seconds` — LLM call latency by provider
- `gopherguard_prs_created_total` — successfully created PRs
- `gopherguard_watched_deployments` — current number of watched Deployments
