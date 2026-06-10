"""
database.py

Sets up the SQLite database and defines the table structure.

SQLite is a file-based database — it creates a single file called
llm_platform.db in your project folder. No separate database server,
no configuration, no installation needed. Perfect for development.

SQLAlchemy is the Python library that lets us interact with that
database using Python classes instead of writing raw SQL.

Two tables are defined here:
    Run    — stores every AI model call with full metadata
    Prompt — stores saved prompts with names and version numbers
"""

from datetime import datetime

from sqlalchemy import (
    create_engine,
    Column,
    String,
    Integer,
    Float,
    Boolean,
    DateTime,
    Text,
)
# Column, String, Integer etc. define what type each database column holds.
# String  = text up to a fixed length (good for names, model names)
# Text    = text of any length (good for prompts and responses)
# Integer = whole numbers (tokens, version numbers)
# Float   = decimal numbers (cost_usd, latency as float if needed)
# Boolean = True or False (success field)
# DateTime = timestamp (created_at)

from sqlalchemy.ext.declarative import declarative_base
# declarative_base() gives us a base class that all our table
# definitions inherit from. It is how SQLAlchemy knows these
# classes represent database tables.

from sqlalchemy.orm import sessionmaker
# sessionmaker creates a factory for database sessions.
# A session is one conversation with the database — you open it,
# do your reads and writes, then close it.


# ─────────────────────────────────────────────────────────────
# DATABASE CONNECTION
# ─────────────────────────────────────────────────────────────

# The database file will be created at this path.
# When you run the server for the first time, SQLAlchemy creates
# llm_platform.db in your project folder automatically.
DATABASE_URL = "sqlite:///./llm_platform.db"

# create_engine connects Python to the database.
# connect_args={"check_same_thread": False} is required for SQLite
# when used with FastAPI because FastAPI handles multiple requests
# concurrently across different threads, but SQLite by default
# only allows one thread to use a connection at a time.
# This setting disables that restriction safely for our use case.
engine = create_engine(
    DATABASE_URL,
    connect_args={"check_same_thread": False}
)

# SessionLocal is a factory — calling SessionLocal() creates a new
# database session. We do not call it here; FastAPI calls it per request.
# autocommit=False means changes are not saved until we explicitly commit.
# autoflush=False means SQLAlchemy waits for us to tell it when to sync.
SessionLocal = sessionmaker(
    autocommit=False,
    autoflush=False,
    bind=engine
)

# Base is the parent class all our table models inherit from.
Base = declarative_base()


# ─────────────────────────────────────────────────────────────
# TABLE 1 — Run
# ─────────────────────────────────────────────────────────────

class Run(Base):
    """
    Stores one AI model call — one row per model per run.
    When a user runs a prompt against 3 models, 3 Run rows are
    inserted — all sharing the same run_id so they can be grouped.

    Example: user runs "What is ML?" against GPT, Llama, Gemini.
    Database gets 3 rows:
        run_id="abc", model="gpt-4o-mini", response="ML is...", ...
        run_id="abc", model="llama-groq",  response="ML is...", ...
        run_id="abc", model="gemini-flash", response="ML is...", ...
    """

    __tablename__ = "runs"
    # __tablename__ is the actual name of the table in the database.
    # SQLAlchemy uses this when generating SQL queries.

    id = Column(Integer, primary_key=True, index=True)
    # Primary key — a unique auto-incrementing integer for each row.
    # index=True makes lookups by this column fast.
    # We use this internally but expose run_id to the outside world.

    run_id = Column(String, index=True, nullable=False)
    # Groups all model results from one user-triggered run together.
    # When user clicks Run with 3 models selected, all 3 rows share
    # the same run_id. This is a UUID string like "a3f2c891-bd47-..."
    # index=True because we frequently query by run_id.

    prompt = Column(Text, nullable=False)
    # The full prompt text that was sent to the model.
    # Text type (not String) because prompts can be very long.

    model = Column(String, nullable=False)
    # Which model produced this result.
    # Example: "gpt-4o-mini", "llama-groq", "gemini-flash"

    response = Column(Text, nullable=True)
    # The text the model returned.
    # nullable=True because if the call failed, there is no response.

    latency_ms = Column(Integer, nullable=False, default=0)
    # How long this model took to respond, in milliseconds.

    input_tokens = Column(Integer, nullable=False, default=0)
    # Tokens in the prompt we sent.

    output_tokens = Column(Integer, nullable=False, default=0)
    # Tokens in the response we received.

    total_tokens = Column(Integer, nullable=False, default=0)
    # input_tokens + output_tokens

    cost_usd = Column(Float, nullable=False, default=0.0)
    # Estimated cost of this call in US dollars.

    success = Column(Boolean, nullable=False, default=False)
    # True if the model responded successfully, False if it errored.

    error = Column(Text, nullable=True)
    # None if success, error message string if failed.

    created_at = Column(DateTime, nullable=False, default=datetime.utcnow)
    # When this run happened. Stored as UTC time.
    # default=datetime.utcnow means SQLAlchemy automatically fills
    # this in with the current time when a row is inserted.

    session_id = Column(String, index=True, nullable=True)
    # Groups all runs from the same chat session together.
    # Generated by the frontend when a new chat starts; all subsequent
    # /run calls within that chat carry the same session_id.

    system_prompt = Column(Text, nullable=True)
    # The system prompt active when this run was made.
    # None if no system prompt was set.


# ─────────────────────────────────────────────────────────────
# CREATE TABLES
# ─────────────────────────────────────────────────────────────

def create_tables():
    """
    Creates all tables in the database if they do not exist yet.

    This is called once when the FastAPI server starts.
    If the tables already exist (from a previous run), this does
    nothing — it does not drop or reset existing data.

    SQLAlchemy looks at all classes that inherit from Base and
    creates a table for each one.
    """
    Base.metadata.create_all(bind=engine)


# ─────────────────────────────────────────────────────────────
# DATABASE SESSION — dependency for FastAPI
# ─────────────────────────────────────────────────────────────

def get_db():
    """
    Creates a database session for one request and closes it after.

    This is a FastAPI dependency — FastAPI calls this function
    automatically for every endpoint that needs database access.

    The 'yield' keyword makes this a generator function.
    Everything before yield runs before the endpoint function.
    Everything after yield runs after the endpoint function finishes.

    So the pattern is:
        1. Open a database session
        2. Give it to the endpoint function (yield db)
        3. Endpoint function runs and does its database work
        4. Session is closed (finally block runs)

    The try/finally ensures the session is always closed even if
    the endpoint raises an exception — no connection leaks.
    """
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()