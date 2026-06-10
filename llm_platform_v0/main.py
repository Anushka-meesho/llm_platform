"""
main.py

The FastAPI application — the HTTP layer that wraps llm_runner.py.

This file does four things:
    1. Defines the FastAPI app and its startup behaviour
    2. Defines all API endpoints (the routes)
    3. Calls llm_runner.run_prompt() to get AI responses
    4. Reads from and writes to the database via SQLAlchemy

Run the server with:
    uvicorn main:app --reload

Then open:
    http://localhost:8000/docs   → interactive Swagger documentation
    http://localhost:8000/redoc  → alternative documentation view
"""

import uuid
import time

from fastapi import FastAPI, Depends, HTTPException, Query
# FastAPI   — the main application class
# Depends   — used for dependency injection (database sessions)
# HTTPException — how you return error responses like 404 or 500
# Query     — used to define URL query parameters like ?page=2

from sqlalchemy.orm import Session
# Session is the type hint for database sessions.

from sqlalchemy import text, func
from database import create_tables, get_db, Run, engine
# create_tables — creates DB tables on startup if they do not exist
# get_db        — FastAPI dependency that opens/closes DB sessions
# Run           — the SQLAlchemy table model

from schemas import (
    RunRequest,
    RunResponse,
    ModelResultResponse,
    SessionListResponse,
    SessionDetailResponse,
    DeleteSessionsRequest,
    DeleteSessionsResponse,
)

from llm_runner import run_prompt, DEFAULT_MODELS
# run_prompt    — the core function from Week 1 that calls the AI models
# DEFAULT_MODELS — the list of 3 model names


# ─────────────────────────────────────────────────────────────
# CREATE THE FASTAPI APPLICATION
# ─────────────────────────────────────────────────────────────

app = FastAPI(
    title="LLM Platform v0",
    description=(
        "A self-serve workbench for comparing LLM responses across "
        "GPT-4o-mini, Llama (Groq), and Gemini Flash simultaneously. "
        "Every run is persisted and retrievable. Prompts can be saved "
        "and versioned."
    ),
    version="0.1.0",
)
# These three fields — title, description, version — show up
# at the top of the Swagger /docs page automatically.


# ─────────────────────────────────────────────────────────────
# STARTUP EVENT
# ─────────────────────────────────────────────────────────────

@app.on_event("startup")
def on_startup():
    """
    Runs once when the server starts.

    Creates the database tables if they do not exist yet.
    If they already exist this does nothing — data is not reset.
    """
    create_tables()
    # Migrate: add session_id column to runs if it was created before this column existed.
    with engine.connect() as conn:
        for col in ["session_id TEXT", "system_prompt TEXT"]:
            try:
                conn.execute(text(f"ALTER TABLE runs ADD COLUMN {col}"))
                conn.commit()
            except Exception:
                pass  # Column already exists
    print("Database tables ready.")


# ─────────────────────────────────────────────────────────────
# HEALTH CHECK
# ─────────────────────────────────────────────────────────────

@app.get("/health", tags=["System"])
def health_check():
    """
    Returns 200 OK if the server is running.

    Used to verify the server started correctly.
    Open http://localhost:8000/health in your browser to check.
    """
    return {
        "status": "ok",
        "models_available": DEFAULT_MODELS,
    }


# ─────────────────────────────────────────────────────────────
# POST /run — THE MAIN ENDPOINT
# ─────────────────────────────────────────────────────────────

@app.post("/run", response_model=RunResponse, tags=["Runs"])
def run_endpoint(
    request: RunRequest,
    db: Session = Depends(get_db),
):
    """
    Run a prompt against multiple LLMs simultaneously.

    Sends the prompt to all selected models at the same time,
    saves every result to the database, and returns the full
    comparison with latency, tokens, and cost per model.

    Args:
        request: RunRequest containing prompt, optional models list,
                 and optional images list.
        db:      Database session — injected automatically by FastAPI
                 via Depends(get_db). You do not pass this manually.

    Returns:
        RunResponse with one ModelResultResponse per model,
        plus overall metrics and a run_id for later retrieval.
    """

    if not request.prompt:
        raise HTTPException(status_code=422, detail="'prompt' must be provided.")
    prompt_text = request.prompt

    # Generate a unique ID for this entire run.
    # All model results from this one user click share this ID.
    # str(uuid.uuid4()) produces something like:
    # "a3f2c891-bd47-4f2a-9c12-e8f2d3b1c4a7"
    run_id = str(uuid.uuid4())

    # Determine which models to run.
    # If the caller did not specify, use all 3 default models.
    models_to_run = request.models if request.models else DEFAULT_MODELS

    # Record start time before calling the AI models.
    # We measure total wall clock time for the whole run.
    overall_start = time.time()

    # Call llm_runner.run_prompt() — this is the Week 1 engine.
    # It runs all models simultaneously and returns a RunResult.
    # This is the single line that does all the AI work.
    try:
        run_result = run_prompt(
            prompt=prompt_text,
            models=models_to_run,
            model_conversations=request.model_conversations,
            temperature=request.temperature if request.temperature is not None else 0.7,
            system_prompt=request.system_prompt,
        )
    except ValueError as e:
        # run_prompt raises ValueError for invalid inputs like empty prompt.
        # We convert it to an HTTP 422 error with a clear message.
        raise HTTPException(status_code=422, detail=str(e))
    except Exception as e:
        # Anything unexpected becomes a 500 Internal Server Error.
        raise HTTPException(
            status_code=500,
            detail=f"Unexpected error running prompt: {str(e)}"
        )

    # Total time for the whole simultaneous run
    total_wall_clock_ms = int((time.time() - overall_start) * 1000)

    # ── Save every model result to the database ──────────────
    # We insert one Run row per model result.
    # All rows share the same run_id so they can be retrieved together.
    for model_result in run_result.results:
        db_run = Run(
            run_id=run_id,
            session_id=request.session_id,
            system_prompt=request.system_prompt,
            prompt=prompt_text,
            model=model_result.model,
            response=model_result.response,
            latency_ms=model_result.latency_ms,
            input_tokens=model_result.input_tokens,
            output_tokens=model_result.output_tokens,
            total_tokens=model_result.total_tokens,
            cost_usd=model_result.cost_usd,
            success=model_result.success,
            error=model_result.error,
        )
        db.add(db_run)
        # db.add() stages the row — queues it for insertion

    db.commit()
    # db.commit() writes all staged rows to the database in one transaction.
    # If any row fails, the whole commit is rolled back automatically.

    # ── Build the response ───────────────────────────────────
    # Convert ModelResult dataclass objects (from llm_runner.py)
    # into ModelResultResponse Pydantic objects (from schemas.py).
    result_responses = [
        ModelResultResponse(
            model=r.model,
            response=r.response,
            latency_ms=r.latency_ms,
            input_tokens=r.input_tokens,
            output_tokens=r.output_tokens,
            total_tokens=r.total_tokens,
            cost_usd=r.cost_usd,
            success=r.success,
            error=r.error,
        )
        for r in run_result.results
    ]
    # This is a list comprehension — it loops through run_result.results
    # and creates one ModelResultResponse for each ModelResult.

    models_succeeded = sum(1 for r in run_result.results if r.success)
    models_failed    = sum(1 for r in run_result.results if not r.success)

    return RunResponse(
        run_id=run_id,
        prompt=prompt_text,
        system_prompt=request.system_prompt,
        results=result_responses,
        total_wall_clock_ms=total_wall_clock_ms,
        models_succeeded=models_succeeded,
        models_failed=models_failed,
    )


# ─────────────────────────────────────────────────────────────
# GET /sessions — LIST ALL CHAT SESSIONS
# ─────────────────────────────────────────────────────────────

@app.get("/sessions", response_model=SessionListResponse, tags=["Sessions"])
def list_sessions(
    page: int = Query(default=1, ge=1),
    page_size: int = Query(default=8, ge=1, le=100),
    db: Session = Depends(get_db),
):
    """
    Returns a paginated list of chat sessions, newest-first.
    Each session groups all /run calls made within one chat.
    """
    offset = (page - 1) * page_size

    rows = (
        db.query(
            Run.session_id,
            func.min(Run.created_at).label("first_at"),
        )
        .filter(Run.session_id.isnot(None))
        .group_by(Run.session_id)
        .order_by(func.max(Run.created_at).desc())
        .offset(offset)
        .limit(page_size)
        .all()
    )

    summaries = []
    for session_id, first_at in rows:
        first_run = (
            db.query(Run)
            .filter(Run.session_id == session_id)
            .order_by(Run.created_at.asc())
            .first()
        )
        turn_count = (
            db.query(Run.run_id)
            .filter(Run.session_id == session_id)
            .distinct()
            .count()
        )
        summaries.append({
            "session_id":   session_id,
            "first_prompt": first_run.prompt[:80] if first_run else "",
            "turn_count":   turn_count,
            "created_at":   first_at,
        })

    total_sessions = (
        db.query(Run.session_id)
        .filter(Run.session_id.isnot(None))
        .distinct()
        .count()
    )

    return {
        "page":           page,
        "page_size":      page_size,
        "total_sessions": total_sessions,
        "total_pages":    max(1, (total_sessions + page_size - 1) // page_size),
        "sessions":       summaries,
    }


# ─────────────────────────────────────────────────────────────
# GET /sessions/{session_id} — FULL CONVERSATION FOR ONE SESSION
# ─────────────────────────────────────────────────────────────

@app.get("/sessions/{session_id}", response_model=SessionDetailResponse, tags=["Sessions"])
def get_session(session_id: str, db: Session = Depends(get_db)):
    """
    Returns every turn in a session in chronological order.
    Each turn contains the user prompt and each model's response.
    """
    runs = (
        db.query(Run)
        .filter(Run.session_id == session_id)
        .order_by(Run.created_at.asc())
        .all()
    )
    if not runs:
        raise HTTPException(status_code=404, detail=f"No session found with id '{session_id}'")

    # Group rows by run_id to reconstruct individual turns
    turns_by_run: dict = {}
    for row in runs:
        if row.run_id not in turns_by_run:
            turns_by_run[row.run_id] = {
                "run_id":        row.run_id,
                "prompt":        row.prompt,
                "system_prompt": row.system_prompt,
                "created_at":    row.created_at,
                "results":       [],
            }
        turns_by_run[row.run_id]["results"].append({
            "model":        row.model,
            "response":     row.response,
            "latency_ms":   row.latency_ms,
            "total_tokens": row.total_tokens,
            "cost_usd":     row.cost_usd,
            "success":      row.success,
            "error":        row.error,
        })

    turns = sorted(turns_by_run.values(), key=lambda t: t["created_at"])
    return {"session_id": session_id, "turns": turns}


# ─────────────────────────────────────────────────────────────
# DELETE /sessions — DELETE ONE OR MORE SESSIONS
# ─────────────────────────────────────────────────────────────

@app.delete("/sessions", response_model=DeleteSessionsResponse, tags=["Sessions"])
def delete_sessions(request: DeleteSessionsRequest, db: Session = Depends(get_db)):
    """
    Deletes all Run rows belonging to the given session IDs.

    The UI sends a list of one session_id at a time. Deleting all Run rows
    for a session_id is equivalent to deleting the session — there is no
    separate sessions table.
    """
    deleted = (
        db.query(Run)
        .filter(Run.session_id.in_(request.session_ids))
        .delete(synchronize_session=False)
    )
    db.commit()
    return DeleteSessionsResponse(
        deleted_count=deleted,
        session_ids=request.session_ids,
    )