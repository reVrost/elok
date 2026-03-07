from __future__ import annotations

import asyncio
import json
import os
import uuid
from pathlib import Path
from typing import Any

from harbor.agents.base import BaseAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

try:
    import websockets
except ImportError as err:  # pragma: no cover - exercised at runtime.
    websockets = None
    _WEBSOCKETS_IMPORT_ERROR: Exception | None = err
else:
    _WEBSOCKETS_IMPORT_ERROR = None


class ElokGatewayAgent(BaseAgent):
    """
    Harbor custom agent that forwards each task instruction to elok's gateway.

    This adapter intentionally benchmarks the `session.send` loop itself.
    It does not execute shell commands inside the task environment.
    """

    @staticmethod
    def name() -> str:
        return "elok-gateway"

    def version(self) -> str:
        return "0.1.0"

    def __init__(
        self,
        logs_dir: Path,
        model_name: str | None = None,
        **kwargs: Any,
    ) -> None:
        self.gateway_url = str(
            kwargs.pop(
                "gateway_url",
                os.getenv("ELOK_GATEWAY_URL", "ws://127.0.0.1:7777/ws"),
            )
        )
        self.tenant_id = str(kwargs.pop("tenant_id", os.getenv("ELOK_TENANT_ID", "default")))
        self.session_prefix = str(kwargs.pop("session_prefix", "harbor"))
        self.send_provider = kwargs.pop("send_provider", None)
        self.send_model = kwargs.pop("send_model", None)
        self.request_timeout_sec = float(
            kwargs.pop("request_timeout_sec", os.getenv("ELOK_REQUEST_TIMEOUT_SEC", "120"))
        )
        super().__init__(logs_dir=logs_dir, model_name=model_name, **kwargs)

    async def setup(self, environment: BaseEnvironment) -> None:
        _ = environment
        if websockets is None:
            raise RuntimeError(
                "Missing Python dependency 'websockets'. Install it in the Harbor "
                "environment (for example: uvx --python 3.12 --with harbor "
                "--with websockets harbor ...)."
            ) from _WEBSOCKETS_IMPORT_ERROR

    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        _ = environment
        result = await self._send_turn(instruction)

        assistant_text = str(result.get("assistant_text", ""))
        session_id = str(result.get("session_id", ""))
        handled_command = bool(result.get("handled_command", False))

        self.logs_dir.mkdir(parents=True, exist_ok=True)
        (self.logs_dir / "instruction.md").write_text(instruction)
        (self.logs_dir / "assistant.txt").write_text(assistant_text)
        (self.logs_dir / "gateway_result.json").write_text(
            json.dumps(result, indent=2, sort_keys=True)
        )

        metadata = dict(context.metadata or {})
        metadata.update(
            {
                "gateway_url": self.gateway_url,
                "tenant_id": self.tenant_id,
                "session_id": session_id,
                "handled_command": handled_command,
                "provider": result.get("provider"),
                "model": result.get("model"),
            }
        )
        context.metadata = metadata

    async def _send_turn(self, instruction: str) -> dict[str, Any]:
        request_id = f"elok-{uuid.uuid4().hex}"
        payload: dict[str, Any] = {
            "type": "call",
            "id": request_id,
            "method": "session.send",
            "params": {
                "session_id": f"{self.session_prefix}-{uuid.uuid4().hex[:12]}",
                "tenant_id": self.tenant_id,
                "text": instruction,
            },
        }
        if self.send_provider:
            payload["params"]["provider"] = str(self.send_provider)
        if self.send_model:
            payload["params"]["model"] = str(self.send_model)

        try:
            async with websockets.connect(
                self.gateway_url,
                open_timeout=self.request_timeout_sec,
                close_timeout=5,
                max_size=10 * 1024 * 1024,
            ) as conn:
                await asyncio.wait_for(
                    conn.send(json.dumps(payload)),
                    timeout=self.request_timeout_sec,
                )
                while True:
                    raw = await asyncio.wait_for(
                        conn.recv(),
                        timeout=self.request_timeout_sec,
                    )
                    envelope = json.loads(raw)
                    if not isinstance(envelope, dict):
                        continue
                    if envelope.get("id") != request_id:
                        continue

                    envelope_type = envelope.get("type")
                    if envelope_type == "error":
                        error_info = envelope.get("error") or {}
                        raise RuntimeError(
                            f"elok gateway error ({error_info.get('code', 'unknown')}): "
                            f"{error_info.get('message', '')}"
                        )
                    if envelope_type != "result":
                        continue

                    result = envelope.get("result")
                    if not isinstance(result, dict):
                        raise RuntimeError("session.send returned a non-object result payload")
                    return result
        except asyncio.TimeoutError as err:
            raise RuntimeError(
                f"timed out waiting for session.send response from {self.gateway_url}"
            ) from err
        except Exception as err:
            raise RuntimeError(f"failed to call elok gateway at {self.gateway_url}") from err

