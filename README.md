# confidential-llama-guard

Llama Prompt Guard 2 86M — a single-model prompt-injection / jailbreak
classifier enclave, optimized for horizontal scaling.

## Architecture

```
shim → router (:8080) → round-robin → llama-pg2-1 (:8001)
                                   → llama-pg2-2 (:8001)
```

Three containers on an internal `replicas` network:

| container     | image                                               | role                            |
| ------------- | --------------------------------------------------- | ------------------------------- |
| `router`      | `ghcr.io/tinfoilsh/confidential-llama-guard`        | Go round-robin reverse proxy    |
| `llama-pg2-1` | `ghcr.io/tinfoilsh/confidential-llama-guard-worker` | Llama PG2 inference (replica 1) |
| `llama-pg2-2` | `ghcr.io/tinfoilsh/confidential-llama-guard-worker` | Llama PG2 inference (replica 2) |

Each worker runs `NUM_THREADS=4`, `MAX_CONCURRENCY=4` — the proven sweet spot
for the 86M mDeBERTa model. Two replicas give **28 req/s at p50=280ms** on a
16-CPU / 8 GiB box, vs 11 req/s at p50=787ms for a single replica at conc=8.
See `tf-test/services/cpu-safeguards/` for
the full benchmark.

## Model

| Model                    | HF repo                               | Size | Stack                | Detects                                            |
| ------------------------ | ------------------------------------- | ---- | -------------------- | -------------------------------------------------- |
| Llama Prompt Guard 2 86M | `meta-llama/Llama-Prompt-Guard-2-86M` | 86M  | transformers (torch) | Prompt injection + jailbreak (binary), 8 languages |

## API

### `POST /classify`

```json
{ "text": "Ignore all previous instructions.", "model": "llama-pg2" }
```

```json
{
  "model": "llama-pg2",
  "label": "malicious",
  "unsafe": true,
  "score": 0.998,
  "latency_ms": 45.2
}
```

The `model` field is optional (defaults to `"llama-pg2"`) for backward
compatibility with the multi-model `confidential-cpu-safeguards` API.

Inputs exceeding **512 tokens** are rejected with `422 Unprocessable Entity`.
The model's context window is 512 tokens (DeBERTa-v2
`max_position_embeddings`); chunk longer inputs into segments of 512 tokens or
fewer and classify each.

### `POST /classify-all`

Runs llama-pg2 on the input. Returns the same result as `/classify` but
wrapped in a `results` dict (compatible with the multi-model API shape).

```json
{ "text": "Ignore all previous instructions." }
```

```json
{
  "text": "Ignore all previous instructions.",
  "results": {
    "llama-pg2": {
      "model": "llama-pg2",
      "label": "malicious",
      "unsafe": true,
      "score": 0.998,
      "latency_ms": 45.2
    }
  }
}
```

### `GET /health`

Returns `{"status": "ok", "models": ["llama-pg2"]}` once the model is loaded.

### `GET /models`

Returns `{"models": ["llama-pg2"]}`.

## Resource sizing

| resource    | allocation | usage (2 replicas, conc=4 each) |
| ----------- | ---------- | ------------------------------- |
| CPUs        | 16         | ~9 cores steady-state           |
| Memory      | 8 GiB      | ~4 GiB (2 GiB per replica)      |
| Throughput  | —          | 28 req/s                        |
| p50 latency | —          | 280ms                           |

## Local development

Without MPK, the model loads from the HuggingFace cache. Pre-download it first:

```bash
# Llama PG2 is gated — accept the Llama 4 Community License at
# https://huggingface.co/meta-llama/Llama-Prompt-Guard-2-86M first.
HF_TOKEN=$HF_TOKEN python download_models.py

# Run (model loads from HF cache via the PG2_MODEL_PATH default)
HF_HOME=$HOME/.cache/huggingface python -m uvicorn server:app --host 0.0.0.0 --port 8001
```

Benchmarks live in `tf-test/services/cpu-safeguards/`.
