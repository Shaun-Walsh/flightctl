# Created by Shaun Walsh
from huggingface_hub import hf_hub_download

# Download pre-quantized GGUF model for llama.cpp
# Q4_K_M is a good balance of quality and size for edge devices
model_repo = "bartowski/Llama-3.2-1B-Instruct-GGUF"
hf_hub_download(
    repo_id=model_repo,
    filename="Llama-3.2-1B-Instruct-Q4_K_M.gguf",
    local_dir="/models",
)
