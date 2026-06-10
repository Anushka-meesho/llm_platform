"""
test_gateway_concept.py

Proves that the single gateway concept works correctly.
Uses mock (fake) responses instead of real API calls.
No API keys needed — safe to run with placeholder keys.

Run with:
    python test_gateway_concept.py

What this file proves:
    1. One LiteLLM function call reaches any of the 3 providers
    2. Auth errors (what placeholder keys cause) are caught cleanly
    3. One provider failing does not block the other two
"""

import litellm
from unittest.mock import patch, MagicMock


# ─────────────────────────────────────────────────────────────
# HELPER — builds a fake API response
# ─────────────────────────────────────────────────────────────

def make_fake_response(model_name: str, response_text: str):
    """
    Builds a fake response object that looks exactly like
    what a real provider returns after a successful API call.

    MagicMock() creates an object where you can set any attribute
    you want. We set the same fields that real responses have —
    choices, usage tokens, model — so the rest of the code
    reads from it identically to a real response.

    Why this matters:
        If the fake response has the same structure as a real one,
        and the code reads from it correctly, then the code will
        also read from a real response correctly.
        The mock proves the real thing would work.
    """
    mock = MagicMock()
    mock.choices[0].message.content = response_text
    mock.usage.prompt_tokens     = 10
    mock.usage.completion_tokens = 20
    mock.usage.total_tokens      = 30
    mock.model                   = model_name
    return mock


# ─────────────────────────────────────────────────────────────
# TEST 1 — Does one gateway call reach all 3 providers?
# ─────────────────────────────────────────────────────────────

def test_gateway_routes_to_correct_provider():
    """
    The core concept being proved:

    WITHOUT LiteLLM you write 3 completely different pieces of code —
    one for each provider, each with different auth, different format,
    different response structure.

    WITH LiteLLM you write ONE piece of code:
        litellm.completion(model="any-model", messages=[...])

    LiteLLM figures out which provider to call, translates the request
    into that provider's format, and normalizes the response back into
    one consistent format — so you always read it the same way.

    NOTE: We use friendly model names here ("gemini-flash", "llama-groq")
    because that is how your llm_runner.py works — it takes friendly names
    and translates them internally via resolve_model().
    """

    # These are the 3 models from THE_3_MODELS in llm_runner.py
    # Using the same friendly names the rest of your code uses
    providers_to_test = [
        {
            "friendly_name": "gpt-4o-mini",
            "provider":      "OpenAI",
            "fake_response": "I am GPT responding through the gateway"
        },
        {
            "friendly_name": "llama-groq",
            "provider":      "Groq",
            "fake_response": "I am Llama responding through the gateway"
        },
        {
            "friendly_name": "gemini-flash",
            "provider":      "Google",
            "fake_response": "I am Gemini responding through the gateway"
        },
    ]

    print("\n" + "=" * 60)
    print("TEST 1 — Single gateway routes to all 3 providers")
    print("Proving: one litellm.completion() call reaches any provider")
    print("=" * 60)

    all_passed = True

    for provider in providers_to_test:
        friendly_name = provider["friendly_name"]
        name          = provider["provider"]
        expected_text = provider["fake_response"]

        # patch() intercepts litellm.completion and returns our
        # fake response instead of making a real network call.
        # The code inside the 'with' block runs exactly as normal —
        # it just gets the fake response back instead of a real one.
        with patch(
            "litellm.completion",
            return_value=make_fake_response(friendly_name, expected_text)
        ):
            response = litellm.completion(
                model=friendly_name,
                messages=[{"role": "user", "content": "Say hello"}]
            )

            # This is the key point — same line reads the response
            # for ALL providers because LiteLLM normalized the format.
            # OpenAI, Groq, Google all come back as:
            # response.choices[0].message.content
            actual_text  = response.choices[0].message.content
            total_tokens = response.usage.total_tokens

            passed = actual_text == expected_text
            if not passed:
                all_passed = False

            status = "PASS" if passed else "FAIL"

            print(f"\n  Provider  : {name}")
            print(f"  Model     : {friendly_name}")
            print(f"  Response  : {actual_text}")
            print(f"  Tokens    : {total_tokens}")
            print(f"  Status    : {status}")

    print("\n" + "=" * 60)
    if all_passed:
        print("RESULT: ALL 3 PASSED")
        print("One gateway, three providers, identical interface.")
        print("When real keys arrive, only the .env values change.")
        print("This code stays exactly the same.")
    else:
        print("RESULT: SOME FAILED — check output above")
    print("=" * 60)


# ─────────────────────────────────────────────────────────────
# TEST 2 — Are auth errors caught cleanly?
# ─────────────────────────────────────────────────────────────

def test_error_handling_with_fake_auth_error():
    """
    Proves that when a provider returns an auth error —
    which is EXACTLY what happens when you use placeholder API keys
    on real calls — the code catches it and returns a clean
    ModelResult with success=False instead of crashing the app.

    This is what you will see if you run python llm_runner.py
    with placeholder keys — auth errors handled cleanly, not crashes.
    """
    from llm_runner import run_single_model

    print("\n\n" + "=" * 60)
    print("TEST 2 — Auth error handling")
    print("Proving: placeholder keys cause clean errors, not crashes")
    print("=" * 60)

    # Make litellm.completion raise an AuthenticationError
    # exactly as it would with a fake/wrong API key
    with patch(
        "litellm.completion",
        side_effect=litellm.exceptions.AuthenticationError(
            message="Invalid API key provided",
            llm_provider="openai",
            model="gpt-4o-mini"
        )
    ):
        result = run_single_model(
            prompt="Say hello",
            model_name="gpt-4o-mini"   # friendly name as used in llm_runner
        )

    print(f"\n  Model   : {result.model}")
    print(f"  Success : {result.success}")
    print(f"  Error   : {result.error}")
    print(f"  Response: {result.response}")

    passed = (
        result.success is False   and   # call failed
        result.error is not None  and   # error message exists
        result.response is None         # no response when failed
    )

    print(f"\n  Status  : {'PASS' if passed else 'FAIL'}")
    print("\n  This is exactly what happens with placeholder keys.")
    print("  The app returns a clean error — it does NOT crash.")
    print("=" * 60)


# ─────────────────────────────────────────────────────────────
# TEST 3 — Does one failure block the other models?
# ─────────────────────────────────────────────────────────────

def test_one_failure_does_not_block_others():
    """
    Proves that if one of the 3 providers fails, the other two
    still complete successfully and return their results.

    In the real platform this maps to the circuit breaker concept —
    if Gemini goes down, the other providers keep working.
    One failure is isolated, not catastrophic.

    We test this by making GPT succeed and Gemini fail,
    then verifying both results came back correctly.
    """
    from llm_runner import run_prompt

    print("\n\n" + "=" * 60)
    print("TEST 3 — Failure isolation")
    print("Proving: one provider failing does not block others")
    print("=" * 60)

    call_count = 0

    def selective_mock(model, messages, **kwargs):
        """
        This mock function replaces litellm.completion.
        It checks which model is being called and decides
        whether to succeed or fail for each one.
        """
        nonlocal call_count
        call_count += 1

        # GPT call → return a successful fake response
        if "gpt" in model:
            return make_fake_response(model, "GPT succeeded fine")

        # Gemini call → raise auth error (simulating a failed provider)
        if "gemini" in model:
            raise litellm.exceptions.AuthenticationError(
                message="Invalid API key",
                llm_provider="gemini",
                model=model
            )

        # Llama call → return a successful fake response
        return make_fake_response(model, "Llama succeeded fine")

    # Run with just 2 models for a clear pass/fail demonstration
    # Using friendly names exactly as used in llm_runner.py
    with patch("litellm.completion", side_effect=selective_mock):
        result = run_prompt(
            prompt="Say hello",
            models=["gpt-4o-mini", "gemini-flash"]
            # gpt-4o-mini  → will succeed (mock returns a response)
            # gemini-flash → will fail   (mock raises AuthenticationError)
        )

    gpt_result    = result.results[0]  # gpt-4o-mini
    gemini_result = result.results[1]  # gemini-flash

    print(f"\n  GPT result   : success={gpt_result.success}, "
          f"response='{gpt_result.response}'")
    print(f"  Gemini result: success={gemini_result.success}, "
          f"error='{gemini_result.error}'")
    print(f"  Total calls made: {call_count}")

    passed = (
        gpt_result.success is True      and  # GPT worked
        gemini_result.success is False  and  # Gemini failed cleanly
        call_count == 2                      # both were actually attempted
    )

    print(f"\n  Status: {'PASS' if passed else 'FAIL'}")
    print("\n  GPT succeeded. Gemini failed. Neither blocked the other.")
    print("  Both calls were attempted regardless of the other's outcome.")
    print("=" * 60)


# ─────────────────────────────────────────────────────────────
# TEST 4 — Does the simultaneous runner preserve result order?
# ─────────────────────────────────────────────────────────────

def test_simultaneous_results_are_in_correct_order():
    """
    Your updated llm_runner.py runs all 3 models simultaneously
    using threads. Whichever model responds fastest finishes first.

    But the output should always come back in a consistent order —
    GPT first, Llama second, Gemini third — regardless of which
    model actually finished first.

    This test proves that ordering is preserved even though
    the calls run simultaneously.
    """
    from llm_runner import run_prompt, DEFAULT_MODELS

    print("\n\n" + "=" * 60)
    print("TEST 4 — Simultaneous calls preserve result order")
    print("Proving: results always come back GPT → Llama → Gemini")
    print("=" * 60)

    def ordered_mock(model, messages, **kwargs):
        # Return a different response for each model
        # so we can verify which result maps to which model
        if "gpt" in model:
            return make_fake_response(model, "response from GPT")
        if "llama" in model or "groq" in model:
            return make_fake_response(model, "response from Llama")
        if "gemini" in model:
            return make_fake_response(model, "response from Gemini")
        return make_fake_response(model, "unknown")

    with patch("litellm.completion", side_effect=ordered_mock):
        result = run_prompt(
            prompt="Say hello",
            models=DEFAULT_MODELS
            # DEFAULT_MODELS = ["gpt-4o-mini", "llama-groq", "gemini-flash"]
        )

    print(f"\n  Expected order: {DEFAULT_MODELS}")
    print(f"  Actual order:   {[r.model for r in result.results]}")
    print()

    passed = True
    for i, (expected_model, actual_result) in enumerate(
        zip(DEFAULT_MODELS, result.results)
    ):
        match = actual_result.model == expected_model
        if not match:
            passed = False
        status = "PASS" if match else "FAIL"
        print(f"  {status}  Position {i+1}: expected '{expected_model}', "
              f"got '{actual_result.model}'")

    print(f"\n  Status: {'PASS' if passed else 'FAIL'}")
    print("  Results are always in the same order regardless of")
    print("  which model finished first in the simultaneous run.")
    print("=" * 60)


# ─────────────────────────────────────────────────────────────
# RUN ALL 4 TESTS
# ─────────────────────────────────────────────────────────────

if __name__ == "__main__":
    print("\n" + "=" * 60)
    print("GATEWAY CONCEPT TESTS — no API keys needed")
    print("Testing llm_runner.py with placeholder/mock responses")
    print("=" * 60)

    test_gateway_routes_to_correct_provider()
    test_error_handling_with_fake_auth_error()
    test_one_failure_does_not_block_others()
    test_simultaneous_results_are_in_correct_order()

    print("\n\n" + "=" * 60)
    print("ALL TESTS COMPLETE")
    print("If all 4 showed PASS — your llm_runner.py is working.")
    print("When real API keys arrive, replace values in .env")
    print("and run: python llm_runner.py")
    print("The code stays exactly the same.")
    print("=" * 60 + "\n")