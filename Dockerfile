# Llama Prompt Guard 2 worker image.
#
#
# Models are mounted as verified model packs (MPK) at boot — read-only
# filesystem at /tinfoil/mpk/. No HuggingFace download or egress required.
# Model paths are set via env vars in tinfoil-config.yml.

FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*

# CPU-only torch + torchvision (from the PyTorch CPU index, not PyPI which serves the
# 2GB+ CUDA wheels by default on Linux). transformers 5.x lazily imports torchvision.
RUN pip install --no-cache-dir torch torchvision --index-url https://download.pytorch.org/whl/cpu

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt

COPY server.py /app/server.py

WORKDIR /app

ENV NUM_THREADS=4 \
    MAX_CONCURRENCY=4

EXPOSE 8001

CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8001"]
