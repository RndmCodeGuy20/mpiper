"""Webhook delivery insertion for the worker."""

import json
from datetime import datetime, timezone


def insert_webhook_deliveries(cur, asset_id: str, job_id, event: str) -> None:
    """Insert pending webhook_deliveries for all registrations matching the asset owner and event."""
    payload = json.dumps({
        "event": event,
        "asset_id": str(asset_id),
        "job_id": int(job_id) if job_id is not None else None,
        "status": event.split(".")[-1],
        "timestamp": datetime.now(timezone.utc).isoformat(),
    })
    cur.execute(
        """
        INSERT INTO webhook_deliveries (registration_id, event, asset_id, job_id, payload)
        SELECT wr.id, %s, %s, %s, %s::jsonb
        FROM webhook_registrations wr
        JOIN assets a ON a.owner_id = wr.user_id
        WHERE a.asset_id = %s
          AND wr.events @> %s::jsonb
        """,
        (event, asset_id, str(job_id), payload, asset_id, json.dumps([event])),
    )
