# LLM Platform v0

Compare GPT-4o-mini, Llama (Groq), and Gemini Flash side by side. Send one prompt, get all three responses simultaneously.

---

## Prerequisites

- Python 3.10+
- API keys for OpenAI, Groq, and Google Gemini

---

## Setup

**1. Clone and create a virtual environment**
```bash
git clone https://github.com/Anushka-meesho/llm_platform.git
cd llm_platform
python3 -m venv venv
source venv/bin/activate        # Windows: venv\Scripts\activate
```

**2. Install dependencies**
```bash
pip install -r requirements.txt
```

**3. Add API keys**

Create a `.env` file in the project root:
```
OPENAI_API_KEY=sk-...
GROQ_API_KEY=gsk_...
GEMINI_API_KEY=AIza...
```

---

## Running the app

The app has two parts that must both be running at the same time. Open two terminals.

**Terminal 1 — start the backend:**
```bash
source venv/bin/activate
uvicorn main:app --reload
```
Backend runs at `http://localhost:8000`

**Terminal 2 — start the frontend:**
```bash
source venv/bin/activate
streamlit run app.py
```
Frontend opens automatically at `http://localhost:8501`

---

## Project structure

```
main.py          — FastAPI backend (API endpoints, database writes)
llm_runner.py    — AI engine (calls all 3 models simultaneously)
app.py           — Streamlit frontend (chat UI)
database.py      — SQLite table definitions
schemas.py       — API request/response shapes
requirements.txt — Python dependencies
.env             — API keys (not committed)
```

---

## API docs

With the backend running, open `http://localhost:8000/docs` for interactive Swagger documentation covering all endpoints.

---

## Running tests

```bash
source venv/bin/activate
pytest test_gateway_concept.py -v
```
