"""
llm_runner.py
The core engine of the LLM Platform v0.

This module has one job:
    Input:  a prompt (string) + list of model names (list of strings)
    Output: one ModelResult per model, containing response, latency,
            tokens, cost, and any error that occurred


MODELS SUPPORTED:
    The platform runs exactly 3 models whose API keys we have:
        1. gpt-4o-mini    (OpenAI)
        2. llama-groq     (Groq)
        3. gemini-flash   (Google)

    All three are called simultaneously on every run.
"""

# ─────────────────────────────────────────────────────────────
# IMPORTS
# ─────────────────────────────────────────────────────────────

import time
# time.time() returns the current time as a float (seconds since 1970)
# We use it to measure how long each API call takes

import concurrent.futures
# concurrent.futures lets us run multiple functions at the same time
# instead of one after another.
# ThreadPoolExecutor creates a pool of threads -Total wait time = slowest model

from dataclasses import dataclass
# dataclass is a Python decorator that makes it easy to define
# classes whose purpose is to hold structured data

from typing import Optional
# Optional[str] means the value is either a string or None

import litellm
# LiteLLM is the single gateway to all AI providers
# It normalizes Gemini, OpenAI, Groq into one consistent interface

from dotenv import load_dotenv
# load_dotenv() reads your .env file and puts the values into
# the environment so os.getenv() can find them


# ─────────────────────────────────────────────────────────────
# LOAD ENVIRONMENT VARIABLES
# ─────────────────────────────────────────────────────────────

# This must be called before anything that reads environment variables.
# It finds the .env file in your project folder and loads it.
load_dotenv()

# Turn off LiteLLM's verbose logging.
# By default LiteLLM prints a lot of debug information to the terminal.
# Setting this to False keeps our output clean.
litellm.set_verbose = False


# ─────────────────────────────────────────────────────────────
# DATA STRUCTURES
# ─────────────────────────────────────────────────────────────
@dataclass
class ModelResult:
    """
    The result of running one prompt against one model.

    Every field has a type annotation so we know exactly
    what kind of data to expect in each box.

    Think of this like a report card for one model's response.
    """

    model: str
    # The friendly name of the model that produced this result.
    # Example: "gpt-4o-mini" or "gemini-flash"

    response: Optional[str]
    # The text the model returned.
    # This is None if the call failed.

    latency_ms: int
    # How long the API call took, in milliseconds.
    # Example: 847 means the call took 0.847 seconds.

    input_tokens: int
    # How many tokens were in the prompt we sent.

    output_tokens: int
    # How many tokens were in the response the model sent back.

    total_tokens: int
    # input_tokens + output_tokens

    cost_usd: float
    # Estimated cost of this call in US dollars.
    # Example: 0.000079 means less than one tenth of a cent.

    error: Optional[str]
    # None if the call succeeded.
    # A human-readable error message if something went wrong.

    success: bool
    # True if we got a valid response, False if we got an error.


@dataclass
class RunResult:
    """
    The complete result of one run — all 3 models combined.

    When the user types a prompt, one RunResult is created
    containing three ModelResults — one per model.
    """
    prompt: str
    # The original prompt that was sent to all models.

    results: list[ModelResult]
    # Always contains exactly 3 items — one per model.


# ─────────────────────────────────────────────────────────────
# THE 3 MODELS WE SUPPORT
# ─────────────────────────────────────────────────────────────

# These are the only 3 models the platform uses.
# Locked to these 3 because these are the API keys we have.
# Key   = friendly name shown in the UI
# Value = exact string LiteLLM needs internally
THE_3_MODELS: dict[str, str] = {
    "gpt-4o-mini":  "gpt-4o-mini",           # OpenAI
    "llama-groq":   "groq/llama-3.3-70b-versatile", # Groq
    "gemini-flash": "gemini/gemini-2.0-flash",  # Google
}

# This is the list used by run_prompt() when no models are specified.
# Always all 3.
DEFAULT_MODELS: list[str] = list(THE_3_MODELS.keys())
# Result: ["gpt-4o-mini", "llama-groq", "gemini-flash"]


def resolve_model(model_name: str) -> str:
    """
    Translate a friendly model name to the exact string LiteLLM needs.

    Looks up in THE_3_MODELS first.
    If not found, passes through unchanged as a fallback.

    Examples:
        resolve_model("gemini-flash") → "gemini/gemini-2.0-flash"
        resolve_model("llama-groq")   → "groq/llama-3.3-70b-versatile"
        resolve_model("gpt-4o-mini")  → "gpt-4o-mini"
    """
    return THE_3_MODELS.get(model_name.lower(), model_name)


def _error_result(model_name: str, start_time: float, error_msg: str) -> ModelResult:
    return ModelResult(
        model=model_name, response=None,
        latency_ms=int((time.time() - start_time) * 1000),
        input_tokens=0, output_tokens=0, total_tokens=0,
        cost_usd=0.0, error=error_msg, success=False,
    )


# ─────────────────────────────────────────────────────────────
# CORE FUNCTION — single model
# ─────────────────────────────────────────────────────────────

def run_single_model(
    prompt: str,
    model_name: str,
    messages: Optional[list[dict]] = None,
    temperature: float = 0.7,
    system_prompt: Optional[str] = None,
) -> ModelResult:
    """
    Call one AI model with the given prompt and return a ModelResult.

    This function is called simultaneously by run_prompt() — once per model.

    Args:
        prompt:      The text to send to the model (used for single-turn and DB storage).
        model_name:  Friendly name like "gemini-flash" or "gpt-4o-mini".
        messages:    Full conversation history for multi-turn runs. When provided,
                     used directly instead of building from prompt.
        temperature: Sampling temperature (0.0 = deterministic, 2.0 = very creative).

    Returns:
        ModelResult — always. Never raises an exception.
        If something goes wrong, success=False and error contains
        a description of what happened.
    """

    # Translate friendly name to what LiteLLM expects internally
    resolved_model = resolve_model(model_name)

    # Record start time BEFORE the API call
    start_time = time.time()

    try:
        if messages:
            # Multi-turn: use the full conversation history.
            # Strip any extra metadata fields (latency, cost, etc.) that the UI
            # stores on messages — the API only accepts role + content.
            msg_list = [{"role": m["role"], "content": m["content"]} for m in messages]
        else:
            msg_list = [{"role": "user", "content": prompt}]

        if system_prompt:
            msg_list = [{"role": "system", "content": system_prompt}] + msg_list

        # The single LiteLLM call.
        # Identical for all 3 providers — only the model string changes.
        response = litellm.completion(
            model=resolved_model,
            messages=msg_list,
            max_tokens=1000,
            temperature=temperature,
        )

        # How long did this specific model take?
        latency_ms = int((time.time() - start_time) * 1000)

        # Extract response text — same line for all 3 providers
        # because LiteLLM normalizes everything to OpenAI's format
        response_text = response.choices[0].message.content

        # Extract token counts
        input_tokens  = response.usage.prompt_tokens
        output_tokens = response.usage.completion_tokens
        total_tokens  = response.usage.total_tokens

        # Calculate cost — wrapped in try/except in case
        # LiteLLM doesn't have pricing data for this model
        try:
            cost = litellm.completion_cost(completion_response=response)
        except Exception:
            cost = 0.0

        return ModelResult(
            model=model_name,
            response=response_text,
            latency_ms=latency_ms,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            total_tokens=total_tokens,
            cost_usd=round(cost, 6),
            error=None,
            success=True,
        )

    except litellm.exceptions.AuthenticationError as e:
        # API key is wrong, missing, or expired.
        return _error_result(model_name, start_time, f"Auth failed for {model_name} — check API key in .env. Details: {str(e)}")

    except litellm.exceptions.RateLimitError as e:
        # Too many requests sent too fast
        return _error_result(model_name, start_time, f"Rate limit hit for {model_name}. Wait and retry. Details: {str(e)}")

    except litellm.exceptions.ServiceUnavailableError as e:
        # Provider servers are down
        return _error_result(model_name, start_time, f"{model_name} is currently unavailable. Details: {str(e)}")

    except Exception as e:
        # Catch-all — network timeout, malformed response, etc.
        return _error_result(model_name, start_time, f"Unexpected error from {model_name}: {type(e).__name__} — {str(e)}")


# ─────────────────────────────────────────────────────────────
# CORE FUNCTION — all 3 models simultaneously
# ─────────────────────────────────────────────────────────────

def run_prompt(
    prompt: str,
    models: list[str] | None = None,
    model_conversations: Optional[dict[str, list[dict]]] = None,
    temperature: float = 0.7,
    system_prompt: Optional[str] = None,
) -> RunResult:
    """
    Run a prompt against all selected models simultaneously and return results.

    All models run at the same time using threads — total wait time equals
    the slowest model, not the sum of all models.

    Args:
        prompt:               The new user message (used for single-turn and DB storage).
        models:               Which models to run. Defaults to all 3.
        model_conversations:  Per-model full conversation history for multi-turn runs.
                              Key = model name, Value = list of {role, content} messages
                              including the new user message at the end.
        temperature:          Sampling temperature passed to every model (0.0–2.0).

    Returns:
        RunResult containing one ModelResult per model,
        in the same order as the models list.
    """

    # Default to all 3 models if none specified
    if models is None:
        models = DEFAULT_MODELS

    # Input validation
    if not prompt or not prompt.strip():
        raise ValueError("Prompt cannot be empty.")

    if not models:
        raise ValueError("At least one model must be selected.")

    # Run all models simultaneously using threads.
    # Results are yielded in completion order (fastest model first).

    results = []

    with concurrent.futures.ThreadPoolExecutor(max_workers=len(models)) as executor:

        futures_map = {
            executor.submit(
                run_single_model,
                prompt,
                model_name,
                model_conversations.get(model_name) if model_conversations else None,
                temperature,
                system_prompt,
            ): model_name
            for model_name in models
        }

        for future in concurrent.futures.as_completed(futures_map):
            results.append(future.result())

    return RunResult(prompt=prompt, results=results)


# ─────────────────────────────────────────────────────────────
# SMOKE TEST
# Runs when you execute: python llm_runner.py
# Shows all 3 models side by side simultaneously
# ─────────────────────────────────────────────────────────────

if __name__ == "__main__":

    print("\n" + "=" * 60)
    print("LLM PLATFORM v0 — 3 Model Simultaneous Run")
    print("=" * 60)
    print("Models: gpt-4o-mini | llama-groq | gemini-flash")
    print("All 3 called at the same time. Total wait = slowest model.")
    print()
    print("With placeholder API keys → auth errors (expected, correct)")
    print("With real API keys        → real responses")
    print("=" * 60 + "\n")

    test_prompt = "In exactly one sentence, what is machine learning?"
    print(f"Prompt: {test_prompt}\n")

    # Time the entire run — should be close to the slowest
    # single model, not the sum of all three
    overall_start = time.time()

    run = run_prompt(prompt=test_prompt)
    # models=None means it uses DEFAULT_MODELS = all 3

    overall_ms = int((time.time() - overall_start) * 1000)

    # Print each model's result
    print("-" * 60)
    for result in run.results:
        print(f"\nMODEL: {result.model.upper()}")

        if result.success:
            print(f"  Response : {result.response}")
            print(f"  Latency  : {result.latency_ms}ms")
            print(f"  Tokens   : in={result.input_tokens}  out={result.output_tokens}  total={result.total_tokens}")
            print(f"  Cost     : ${result.cost_usd}")
        else:
            print(f"  STATUS   : Failed (expected with placeholder keys)")
            print(f"  ERROR    : {result.error}")

        print("-" * 60)

    # Show total wall clock time
    # If running sequentially this would be sum of all latencies.
    # Running simultaneously it should be close to the slowest model.
    print(f"\nTotal wall-clock time: {overall_ms}ms")
    print("(simultaneous — not the sum of individual latencies)")

    # Summary line
    success_count = sum(1 for r in run.results if r.success)
    print(f"\nResult: {success_count}/3 models succeeded")

    if success_count == 0:
        print("All failed — placeholder keys are working correctly.")
        print("Replace values in .env with real keys to get responses.")
    elif success_count == 3:
        print("All 3 models responded successfully.")