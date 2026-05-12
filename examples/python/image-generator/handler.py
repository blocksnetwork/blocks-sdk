"""
Image Generator handler -- generates images from text prompts using Google Gemini API.

Mirrors the Node image-generator example handler.

Requires GEMINI_API_KEY or GOOGLE_API_KEY environment variable.

Input format:
  { "kind": "image_prompt", "prompt": "A cozy reading nook..." }

Output: image bytes as artifact
"""

from __future__ import annotations

import base64
import json
import os
from typing import Any, Dict, Optional
from urllib.request import Request, urlopen
from urllib.error import HTTPError

from blocks_network.types import StartTaskMessage, TaskContext

DEFAULT_MODEL = "gemini-3-pro-image-preview"


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    """Image generator handler - generates images from text prompts via Gemini."""
    prompt_input = _extract_image_prompt(task.request_parts or [])

    if not prompt_input:
        return {
            "artifact": json.dumps({
                "ok": False,
                "error": "Missing image_prompt with prompt field",
                "example": {"kind": "image_prompt", "prompt": "A futuristic city at sunset"},
            }, indent=2),
            "mimeType": "application/json",
        }

    prompt = prompt_input["prompt"]
    model = prompt_input["model"]
    preview = prompt[:50] + "..." if len(prompt) > 50 else prompt

    print(f"[ImageGenerator] Generating image for prompt: {prompt[:100]}...")
    if ctx:
        ctx.report_status(f'Generating image: "{preview}"')

    try:
        data, mime_type = _generate_image(prompt, model)
        print(f"[ImageGenerator] Generated image: {len(data)} bytes, {mime_type}")
        return {"artifact": data, "mimeType": mime_type}
    except Exception as err:
        return {
            "artifact": json.dumps({
                "ok": False,
                "error": str(err),
                "prompt": prompt,
            }, indent=2),
            "mimeType": "application/json",
        }


# ---------------------------------------------------------------------------
# Gemini API client
# ---------------------------------------------------------------------------


def _generate_image(prompt: str, model: str) -> tuple[bytes, str]:
    """Call Gemini API to generate an image and return (bytes, mime_type)."""
    api_key = os.environ.get("GOOGLE_API_KEY") or os.environ.get("GEMINI_API_KEY")
    if not api_key:
        raise RuntimeError(
            "Missing API key. Set GEMINI_API_KEY or GOOGLE_API_KEY environment variable."
        )

    endpoint = (
        f"https://generativelanguage.googleapis.com/v1beta/models/{model}"
        f":generateContent?key={api_key}"
    )

    payload = json.dumps({
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {"responseModalities": ["IMAGE"]},
    }).encode()

    req = Request(endpoint, data=payload, headers={"Content-Type": "application/json"})

    try:
        with urlopen(req) as resp:
            result = json.loads(resp.read())
    except HTTPError as e:
        error_text = e.read().decode() if e.fp else str(e)
        raise RuntimeError(f"Gemini API error: {e.code} - {error_text}") from e

    # Extract image from response
    for candidate in result.get("candidates", []):
        for part in candidate.get("content", {}).get("parts", []):
            # Support both camelCase and snake_case response shapes
            inline_data = part.get("inlineData") or part.get("inline_data")
            if inline_data and inline_data.get("data"):
                mime = (
                    inline_data.get("mimeType")
                    or inline_data.get("mime_type")
                    or "image/png"
                )
                return base64.b64decode(inline_data["data"]), mime

    raise RuntimeError("No image returned from Gemini API. Try a more detailed prompt.")


# ---------------------------------------------------------------------------
# Input parsing
# ---------------------------------------------------------------------------


def _extract_image_prompt(parts: list) -> Optional[Dict[str, str]]:
    """Extract prompt and model from request parts."""
    for part in parts:
        if not isinstance(part, dict):
            continue
        prompt = part.get("prompt") or part.get("text")
        if isinstance(prompt, str) and prompt:
            return {
                "prompt": prompt,
                "model": part.get("model", DEFAULT_MODEL),
            }
    return None
