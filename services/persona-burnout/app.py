import json
import os
import threading
import uuid
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo

import requests
from flask import Flask, jsonify, request

app = Flask(__name__)

PORT = int(os.getenv("PORT", "8098"))
PERSONA_TOKEN = os.getenv("NEXORA_PERSONA_TOKEN", "persona-ai-token").strip()
CHAT_BASE_URL = os.getenv("NEXORA_CHAT_BASE_URL", "http://nexora-chat:8086").rstrip("/")
CHAT_SHIELD_TOKEN = os.getenv("NEXORA_CHAT_SHIELD_TOKEN", "persona-ai-token").strip()
BUSINESS_BASE_URL = os.getenv("NEXORA_BUSINESS_BASE_URL", "http://nexora-business:8091").rstrip("/")
BUSINESS_SHIELD_TOKEN = os.getenv("NEXORA_BUSINESS_SHIELD_TOKEN", "persona-ai-token").strip()
STOCK_BASE_URL = os.getenv("NEXORA_STOCK_BASE_URL", "http://nexora-stock:8087").rstrip("/")
SOCIAL_BASE_URL = os.getenv("NEXORA_SOCIAL_BASE_URL", "http://nexora-social:8084").rstrip("/")
SOCIAL_INGEST_TOKEN = os.getenv("SOCIAL_INGEST_TOKEN", "social-ingest-token").strip()
STATE_FILE = os.getenv("PERSONA_STATE_FILE", "/data/persona-state.json")
TIMEZONE_NAME = os.getenv("PERSONA_TIMEZONE", "America/Sao_Paulo").strip()
REQUEST_TIMEOUT = float(os.getenv("PERSONA_HTTP_TIMEOUT_SEC", "4"))
STRESS_BPM_THRESHOLD = int(os.getenv("STRESS_BPM_THRESHOLD", "110"))
STRESS_SCORE_THRESHOLD = float(os.getenv("STRESS_SCORE_THRESHOLD", "0.75"))
MAX_SOCIAL_PUSH = int(os.getenv("MAX_SOCIAL_PUSH", "3"))

state_lock = threading.Lock()
last_error = ""


def ensure_state_file() -> None:
    folder = os.path.dirname(STATE_FILE)
    if folder:
        os.makedirs(folder, exist_ok=True)
    if not os.path.exists(STATE_FILE):
        base = {"users": {}, "updated_at": now_utc_iso()}
        with open(STATE_FILE, "w", encoding="utf-8") as f:
            json.dump(base, f, ensure_ascii=True, indent=2)


def load_state() -> dict:
    ensure_state_file()
    with open(STATE_FILE, "r", encoding="utf-8") as f:
        raw = f.read().strip()
    if not raw:
        return {"users": {}, "updated_at": now_utc_iso()}
    parsed = json.loads(raw)
    if not isinstance(parsed, dict):
        return {"users": {}, "updated_at": now_utc_iso()}
    if "users" not in parsed or not isinstance(parsed["users"], dict):
        parsed["users"] = {}
    return parsed


def save_state(state: dict) -> None:
    state["updated_at"] = now_utc_iso()
    tmp = f"{STATE_FILE}.tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(state, f, ensure_ascii=True, indent=2)
    os.replace(tmp, STATE_FILE)


def now_utc_iso() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def parse_event_time(raw: str | None) -> datetime:
    if not raw:
        return datetime.now(timezone.utc)
    text = str(raw).strip()
    try:
        if text.endswith("Z"):
            text = text[:-1] + "+00:00"
        dt = datetime.fromisoformat(text)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt.astimezone(timezone.utc)
    except Exception:
        return datetime.now(timezone.utc)


def local_time(utc_dt: datetime) -> datetime:
    try:
        return utc_dt.astimezone(ZoneInfo(TIMEZONE_NAME))
    except Exception:
        return utc_dt.astimezone(timezone.utc)


def is_weekend(utc_dt: datetime) -> bool:
    local = local_time(utc_dt)
    return local.weekday() >= 5


def next_monday_morning(utc_dt: datetime) -> datetime:
    local = local_time(utc_dt)
    days_ahead = (7 - local.weekday()) % 7
    if days_ahead == 0:
        days_ahead = 7
    monday = (local + timedelta(days=days_ahead)).replace(hour=8, minute=0, second=0, microsecond=0)
    return monday.astimezone(timezone.utc)


def normalize_id(v: str | None) -> str:
    raw = str(v or "").strip().lower()
    if not raw:
        return ""
    out = []
    last_dash = False
    for ch in raw:
        if ch.isalnum():
            out.append(ch)
            last_dash = False
        elif not last_dash:
            out.append("-")
            last_dash = True
    return "".join(out).strip("-")


def classify_mood(bpm: int, stress_score: float) -> str:
    if bpm >= 130 or stress_score >= 0.8:
        return "stressed"
    if bpm <= 80 and stress_score <= 0.35:
        return "calm"
    if bpm >= 95 and stress_score <= 0.55:
        return "energized"
    return "focused"


def mood_to_category(mood: str) -> str:
    mapping = {
        "stressed": "wellness",
        "calm": "home",
        "focused": "office",
        "energized": "fitness",
    }
    return mapping.get(mood, "lifestyle")


def require_persona_token() -> tuple[bool, tuple]:
    if not PERSONA_TOKEN:
        return True, tuple()
    token = str(request.headers.get("x-persona-token", "")).strip()
    if token != PERSONA_TOKEN:
        return False, (jsonify({"error": "invalid persona token"}), 401)
    return True, tuple()


def call_json(method: str, url: str, payload: dict | None = None, headers: dict | None = None) -> tuple[int, dict | str]:
    final_headers = {"accept": "application/json"}
    if payload is not None:
        final_headers["content-type"] = "application/json"
    if headers:
        final_headers.update(headers)

    resp = requests.request(method=method, url=url, json=payload, headers=final_headers, timeout=REQUEST_TIMEOUT)
    text = resp.text or ""
    if not text:
        return resp.status_code, {}
    try:
        return resp.status_code, resp.json()
    except Exception:
        return resp.status_code, text


def propagate_shield(user_id: str, blocked: bool, reason: str, until_iso: str) -> dict:
    outcomes = {}

    payload = {
        "user_id": user_id,
        "blocked": blocked,
        "reason": reason,
        "until": until_iso,
    }

    try:
        status, body = call_json(
            "POST",
            f"{CHAT_BASE_URL}/v1/chat/policy/shield",
            payload,
            {"x-shield-token": CHAT_SHIELD_TOKEN},
        )
        outcomes["chat"] = {"status": status, "body": body}
    except Exception as exc:
        outcomes["chat"] = {"status": 0, "error": str(exc)}

    try:
        status, body = call_json(
            "POST",
            f"{BUSINESS_BASE_URL}/v1/business/policy/shield",
            payload,
            {"x-shield-token": BUSINESS_SHIELD_TOKEN},
        )
        outcomes["business"] = {"status": status, "body": body}
    except Exception as exc:
        outcomes["business"] = {"status": 0, "error": str(exc)}

    return outcomes


def refresh_social_recommendations(user_id: str, mood: str) -> dict:
    category = mood_to_category(mood)
    summary = {
        "mood": mood,
        "category": category,
        "stock_count": 0,
        "pushed_count": 0,
        "items": [],
    }

    try:
        status, body = call_json(
            "GET",
            f"{STOCK_BASE_URL}/v1/products/suggestions?source=all&category={category}&limit={MAX_SOCIAL_PUSH}",
        )
        if status < 200 or status > 299 or not isinstance(body, dict):
            return summary
        items = body.get("data") if isinstance(body.get("data"), list) else []
        summary["stock_count"] = len(items)
        if not items:
            return summary

        audience = "personal" if mood == "stressed" else "all"
        for idx, item in enumerate(items[:MAX_SOCIAL_PUSH], start=1):
            title = str(item.get("title") or f"Produto {idx}").strip()
            source = str(item.get("source") or "dropship").strip().lower()
            ext_id = str(item.get("external_id") or str(uuid.uuid4())[:8]).strip().lower()
            video_id = f"mood-{normalize_id(user_id)}-{int(datetime.now().timestamp())}-{idx}-{ext_id}"

            payload = {
                "id": video_id,
                "creator_id": "persona-ai",
                "title": f"{mood.title()} Pick: {title}",
                "description": f"Sugestao automatica para humor {mood} com foco em conversao dropship.",
                "object_key": f"ai-recommendations/{mood}/{video_id}.mp4",
                "duration_seconds": 18 + idx,
                "audience": audience,
                "tags": ["persona-ai", "dropship", mood, source, category],
                "monetization": {"enabled": True, "model": "ad_cpm", "cpm_usd": 1.2, "rev_share_pct": 55.0},
            }
            try:
                p_status, p_body = call_json(
                    "POST",
                    f"{SOCIAL_BASE_URL}/v1/videos",
                    payload,
                    {"x-ingest-token": SOCIAL_INGEST_TOKEN},
                )
                if 200 <= p_status <= 299:
                    summary["pushed_count"] += 1
                    summary["items"].append({
                        "video_id": video_id,
                        "title": payload["title"],
                        "source": source,
                    })
                elif p_status == 409:
                    summary["items"].append({"video_id": video_id, "status": "duplicate"})
                else:
                    summary["items"].append({"video_id": video_id, "status": "failed", "http": p_status, "body": p_body})
            except Exception as exc:
                summary["items"].append({"video_id": video_id, "status": "error", "error": str(exc)})
    except Exception:
        return summary

    return summary


@app.get("/healthz")
def healthz():
    with state_lock:
        state = load_state()
    return jsonify(
        {
            "status": "ok",
            "service": "persona-burnout",
            "users": len(state.get("users", {})),
            "stress_bpm_threshold": STRESS_BPM_THRESHOLD,
            "stress_score_threshold": STRESS_SCORE_THRESHOLD,
            "last_error": last_error,
        }
    )


@app.get("/v1/shield/status")
def shield_status():
    user_id = normalize_id(request.args.get("user_id"))
    if not user_id:
        return jsonify({"error": "user_id is required"}), 400

    with state_lock:
        state = load_state()
        user = state.get("users", {}).get(user_id, {})

    return jsonify(
        {
            "user_id": user_id,
            "shield_active": bool(user.get("shield_active", False)),
            "shield_reason": str(user.get("shield_reason", "")),
            "shield_until": str(user.get("shield_until", "")),
            "mood": str(user.get("mood", "unknown")),
            "last_heartbeat": user.get("last_heartbeat", {}),
        }
    )


@app.get("/v1/mood/recommendations")
def mood_recommendations():
    user_id = normalize_id(request.args.get("user_id"))
    if not user_id:
        return jsonify({"error": "user_id is required"}), 400

    with state_lock:
        state = load_state()
        user = state.get("users", {}).get(user_id, {})

    return jsonify(
        {
            "user_id": user_id,
            "mood": str(user.get("mood", "unknown")),
            "recommendations": user.get("recommendations", []),
        }
    )


@app.post("/v1/signals/heartbeat")
def ingest_heartbeat():
    ok, err = require_persona_token()
    if not ok:
        return err

    payload = request.get_json(silent=True) or {}
    user_id = normalize_id(payload.get("user_id"))
    if not user_id:
        return jsonify({"error": "user_id is required"}), 400

    bpm = int(payload.get("bpm") or 0)
    if bpm < 0:
        bpm = 0
    stress_score = float(payload.get("stress_score") or 0.0)
    stress_score = max(0.0, min(1.0, stress_score))
    source = str(payload.get("source") or "unknown").strip().lower() or "unknown"

    event_at = parse_event_time(payload.get("event_at"))
    weekend = is_weekend(event_at)
    high_stress = bpm >= STRESS_BPM_THRESHOLD or stress_score >= STRESS_SCORE_THRESHOLD
    mood = classify_mood(bpm, stress_score)

    shield_active = bool(weekend and high_stress)
    shield_reason = ""
    shield_until = ""
    propagation = {}

    if shield_active:
        until = next_monday_morning(event_at)
        shield_until = until.replace(microsecond=0).isoformat()
        shield_reason = "burnout_shield_weekend_high_stress"

    with state_lock:
        state = load_state()
        users = state.setdefault("users", {})
        user = users.setdefault(user_id, {})

        previously_active = bool(user.get("shield_active", False))

        user["mood"] = mood
        user["last_heartbeat"] = {
            "bpm": bpm,
            "stress_score": stress_score,
            "source": source,
            "event_at": event_at.replace(microsecond=0).isoformat(),
        }
        user["shield_active"] = shield_active
        user["shield_reason"] = shield_reason
        user["shield_until"] = shield_until

        if shield_active or previously_active:
            propagation = propagate_shield(
                user_id=user_id,
                blocked=shield_active,
                reason=shield_reason if shield_active else "shield_released",
                until_iso=shield_until,
            )

        social = refresh_social_recommendations(user_id, mood)
        recs = user.get("recommendations", [])
        if not isinstance(recs, list):
            recs = []
        recs.insert(0, {"at": now_utc_iso(), **social})
        user["recommendations"] = recs[:50]

        save_state(state)

    return jsonify(
        {
            "status": "processed",
            "user_id": user_id,
            "mood": mood,
            "weekend": weekend,
            "high_stress": high_stress,
            "shield_active": shield_active,
            "shield_reason": shield_reason,
            "shield_until": shield_until,
            "propagation": propagation,
            "social": social,
        }
    )


@app.post("/v1/shield/override")
def shield_override():
    ok, err = require_persona_token()
    if not ok:
        return err

    payload = request.get_json(silent=True) or {}
    user_id = normalize_id(payload.get("user_id"))
    blocked = bool(payload.get("blocked", False))
    reason = str(payload.get("reason") or "manual_override").strip()
    until = str(payload.get("until") or "").strip()

    if not user_id:
        return jsonify({"error": "user_id is required"}), 400

    with state_lock:
        state = load_state()
        user = state.setdefault("users", {}).setdefault(user_id, {})
        user["shield_active"] = blocked
        user["shield_reason"] = reason if blocked else ""
        user["shield_until"] = until if blocked else ""
        save_state(state)

    propagation = propagate_shield(user_id=user_id, blocked=blocked, reason=reason, until_iso=until)
    return jsonify(
        {
            "status": "updated",
            "user_id": user_id,
            "shield_active": blocked,
            "shield_reason": reason,
            "shield_until": until,
            "propagation": propagation,
        }
    )


@app.post("/v1/social/recommendations/refresh")
def social_refresh():
    ok, err = require_persona_token()
    if not ok:
        return err

    payload = request.get_json(silent=True) or {}
    user_id = normalize_id(payload.get("user_id"))
    mood = str(payload.get("mood") or "focused").strip().lower()
    if not user_id:
        return jsonify({"error": "user_id is required"}), 400

    social = refresh_social_recommendations(user_id, mood)

    with state_lock:
        state = load_state()
        user = state.setdefault("users", {}).setdefault(user_id, {})
        recs = user.get("recommendations", [])
        if not isinstance(recs, list):
            recs = []
        recs.insert(0, {"at": now_utc_iso(), **social})
        user["recommendations"] = recs[:50]
        user["mood"] = mood
        save_state(state)

    return jsonify({"status": "refreshed", "user_id": user_id, "social": social})


if __name__ == "__main__":
    ensure_state_file()
    app.run(host="0.0.0.0", port=PORT)
