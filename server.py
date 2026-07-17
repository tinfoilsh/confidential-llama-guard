"""Llama Prompt Guard 2 inference server.

A single-model safeguard enclave: Meta's Llama Prompt Guard 2 86M, a binary
prompt-injection / jailbreak classifier (86M params, transformers, 8 languages).

Optimized for horizontal scaling: run 2+ replicas behind the Go round-robin
router (see router/).

Models are mounted as verified model packs (MPK) at boot — read-only
filesystem at /tinfoil/mpk/. No HuggingFace download or egress required.
"""

import asyncio
import logging
import os
import time
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from starlette.concurrency import run_in_threadpool

log = logging.getLogger("llama-pg2")
logging.basicConfig(level=logging.INFO)

models: dict[str, Any] = {}
semaphores: dict[str, asyncio.Semaphore] = {}


def load_models() -> None:
    n_threads = int(os.environ.get("NUM_THREADS", str(os.cpu_count() or 1)))
    max_concurrency = int(os.environ.get("MAX_CONCURRENCY", "1"))

    import torch

    torch.set_num_threads(n_threads)

    from transformers import AutoModelForSequenceClassification, AutoTokenizer

    path = os.environ.get("PG2_MODEL_PATH", "meta-llama/Llama-Prompt-Guard-2-86M")
    log.info("Loading Llama Prompt Guard 2 86M from %s...", path)
    tokenizer = AutoTokenizer.from_pretrained(path)
    model = AutoModelForSequenceClassification.from_pretrained(path)
    model.eval()

    # Warmup
    inputs = tokenizer("warmup", return_tensors="pt", truncation=True, max_length=512)
    with torch.no_grad():
        model(**inputs)

    models["llama-pg2"] = {"tokenizer": tokenizer, "model": model}
    semaphores["llama-pg2"] = asyncio.Semaphore(max_concurrency)
    log.info(
        "Llama Prompt Guard 2 86M loaded (threads=%d, max_concurrency=%d)",
        n_threads,
        max_concurrency,
    )


@asynccontextmanager
async def lifespan(app: FastAPI):
    load_models()
    yield


app = FastAPI(title="Llama Prompt Guard 2", lifespan=lifespan)


# --- Request / response models ---


class ClassifyRequest(BaseModel):
    text: str
    model: str = "llama-pg2"


class ClassifyResponse(BaseModel):
    model: str
    label: str
    unsafe: bool
    score: float | None = None
    latency_ms: float


class ClassifyAllRequest(BaseModel):
    text: str


# --- Classify ---


def _classify_llama_pg2(text: str) -> dict:
    import torch

    entry = models["llama-pg2"]
    tokenizer = entry["tokenizer"]
    model = entry["model"]
    inputs = tokenizer(text, return_tensors="pt", truncation=True, max_length=512)
    with torch.no_grad():
        logits = model(**inputs).logits
    scores = torch.softmax(logits, dim=-1)
    pred_id = logits.argmax().item()
    # Llama PG2 config has generic LABEL_0/LABEL_1 — map to benign/malicious.
    raw_label = model.config.id2label[pred_id].lower()
    label = (
        "malicious"
        if pred_id == 1
        else "benign"
        if raw_label.startswith("label")
        else raw_label
    )
    return {
        "label": label,
        "unsafe": label == "malicious",
        "score": scores[0][pred_id].item(),
    }


# --- Endpoints ---


@app.get("/health")
def health():
    return {"status": "ok", "models": sorted(models.keys())}


@app.get("/models")
def list_models():
    return {"models": sorted(models.keys())}


@app.post("/classify", response_model=ClassifyResponse)
async def classify(req: ClassifyRequest):
    if req.model != "llama-pg2":
        raise HTTPException(
            400,
            f"Unknown model: {req.model}. Available: ['llama-pg2']",
        )
    if "llama-pg2" not in models:
        raise HTTPException(503, "Model llama-pg2 failed to load. Check server logs.")
    async with semaphores["llama-pg2"]:
        start = time.perf_counter()
        result = await run_in_threadpool(_classify_llama_pg2, req.text)
        result["latency_ms"] = (time.perf_counter() - start) * 1000
        result["model"] = "llama-pg2"
        return result


@app.post("/classify-all")
async def classify_all(req: ClassifyAllRequest):
    async with semaphores["llama-pg2"]:
        start = time.perf_counter()
        try:
            result = await run_in_threadpool(_classify_llama_pg2, req.text)
            result["latency_ms"] = (time.perf_counter() - start) * 1000
            result["model"] = "llama-pg2"
            return {"text": req.text, "results": {"llama-pg2": result}}
        except Exception as e:
            return {
                "text": req.text,
                "results": {
                    "llama-pg2": {
                        "error": str(e),
                        "latency_ms": (time.perf_counter() - start) * 1000,
                    }
                },
            }
