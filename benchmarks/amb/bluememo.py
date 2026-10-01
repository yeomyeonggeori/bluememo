import os
import urllib.error
import urllib.request
import json
from pathlib import Path

from ..models import Document
from .base import MemoryProvider


class BluememoMemoryProvider(MemoryProvider):
    name = "bluememo"
    description = "Atomic facts in one SQLite file per person, recalled by three fused lanes with no model call."
    kind = "local"
    variant = "http"
    link = "https://github.com/yeomyeonggeori/bluememo"
    concurrency = 4

    def __init__(self):
        self._base = os.environ.get("BLUEMEMO_SERVER", "http://127.0.0.1:8713")
        self._sources = os.environ.get("BLUEMEMO_SOURCES", "") != ""
        self._recall = int(os.environ.get("BLUEMEMO_RECALL", "0"))

    def _post(self, route: str, payload: dict) -> dict | None:
        request = urllib.request.Request(
            f"{self._base}{route}",
            data=json.dumps(payload).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=1800) as response:
            body = response.read()
        return json.loads(body) if body else None

    def initialize(self) -> None:
        try:
            with urllib.request.urlopen(f"{self._base}/health", timeout=10) as response:
                response.read()
        except urllib.error.URLError as failure:
            raise RuntimeError(
                f"no bluememo server at {self._base}: start cmd/bench-server with -directory"
            ) from failure

    def prepare(self, store_dir: Path, unit_ids: set[str] | None = None, reset: bool = True) -> None:
        if reset:
            self._post("/reset", {})

    @staticmethod
    def _as_text(content: str) -> str:
        """A document whose content is a list of turns is rendered as the lines a
        reader would see. bluememo takes a note of what was said, so a JSON array
        reaches its decomposition as punctuation. Anything else passes through."""
        try:
            parsed = json.loads(content)
        except (ValueError, TypeError):
            return content
        if not isinstance(parsed, list) or not parsed:
            return content
        lines = []
        for turn in parsed:
            if not isinstance(turn, dict):
                return content
            speaker = turn.get("speaker") or turn.get("role") or turn.get("name")
            said = turn.get("text") or turn.get("content") or turn.get("message")
            if said is None:
                return content
            lines.append(f"{speaker}: {said}" if speaker else str(said))
        return "\n".join(lines)

    def ingest(self, documents: list[Document]) -> None:
        self._post("/ingest", {"documents": [
            {
                "id": document.id,
                "content": self._as_text(document.content),
                "user_id": document.user_id or "shared",
                "timestamp": document.timestamp or "",
            }
            for document in documents
        ]})

    def retrieve(self, query: str, k: int = 10, user_id: str | None = None, query_timestamp: str | None = None) -> tuple[list[Document], dict | None]:
        answer = self._post("/retrieve", {
            "query": query,
            "k": self._recall or k,
            "user_id": user_id or "shared",
            "sources": self._sources,
        }) or {}
        memories = answer.get("memories", [])
        documents = [
            Document(
                id=memory["id"],
                content=memory["content"],
                user_id=user_id,
                source_ids=memory.get("source_ids"),
            )
            for memory in memories
        ]
        return documents, answer
