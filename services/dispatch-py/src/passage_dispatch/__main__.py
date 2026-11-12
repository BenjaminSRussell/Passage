from __future__ import annotations

import json
import logging
import os
import sys

import uvicorn

from .api import SERVICE, create_app
from .auth import ServiceVerifier
from .logpseudo import install as install_log_pseudonyms

RESERVED = set(logging.makeLogRecord({}).__dict__) | {"message", "asctime"}


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        line = {
            "timestamp": self.formatTime(record, "%Y-%m-%dT%H:%M:%S%z"),
            "level": record.levelname,
            "message": record.getMessage(),
            "service": SERVICE,
            **{k: v for k, v in record.__dict__.items() if k not in RESERVED},
        }
        if record.exc_info:
            line["exception"] = self.formatException(record.exc_info)
        elif record.exc_text:
            line["exception"] = record.exc_text
        return json.dumps(line, default=str)


def main() -> None:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())
    install_log_pseudonyms(handler, os.environ)  # eu-data-boundary or LOG_PSEUDONYMIZE=true (#605)
    logging.basicConfig(level=logging.INFO, handlers=[handler], force=True)
    verifier = ServiceVerifier.from_env(os.environ.get)
    if verifier is None:
        logging.getLogger("passage_dispatch").error("PASSAGE_SERVICE_PUBLIC_KEYS is not set; every request will be refused")
    uvicorn.run(create_app(verifier), host=os.environ.get("HTTP_HOST", "0.0.0.0"), port=int(os.environ.get("HTTP_PORT", "8080")), log_config=None)


if __name__ == "__main__":
    main()
