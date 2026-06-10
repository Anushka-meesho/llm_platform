"""
app.py
Streamlit front-end for the LLM Comparison Platform.

Requires the FastAPI backend running first:
    uvicorn main:app --reload

Then start this UI:
    streamlit run app.py
"""

import base64 as _b64
import html as _html
import uuid
import streamlit as st
import streamlit.components.v1 as components
import requests

BASE_URL = "http://localhost:8000"
MODELS = ["gpt-4o-mini", "llama-groq", "gemini-flash"]

st.set_page_config(
    page_title="LLM Platform",
    page_icon="⚡",
    layout="wide",
)

st.markdown("""
<style>
/* ── disable page-level scroll ────────────────────────────── */
body, .main, div[data-testid="stAppViewContainer"] {
    overflow: hidden !important;
}

/* ── main content padding (updated dynamically by JS) ─────── */
div[data-testid="stMainBlockContainer"] {
    padding-bottom: 220px !important;
    max-width: 100% !important;
    overflow: hidden !important;
}

/* ── Expander = TOP half of unified card ─────────────────── */
div[data-testid="stExpander"] {
    position: fixed !important;
    z-index: 100 !important;
    background: transparent !important;
    border: none !important;
    box-shadow: none !important;
    margin: 0 !important;
    padding: 0 !important;
}

div[data-testid="stExpander"] > details {
    background: #F2F2F4 !important;
    border: 1px solid #E6E6EA !important;
    border-bottom: none !important;
    border-radius: 14px 14px 0 0 !important;
    overflow: hidden !important;
    margin: 0 !important;
    box-shadow: none !important;
}

div[data-testid="stExpander"] details > summary {
    display: flex !important;
    align-items: center !important;
    gap: 10px !important;
    padding: 11px 18px !important;
    background: #F2F2F4 !important;
    list-style: none !important;
    cursor: pointer !important;
    user-select: none !important;
    border-bottom: 1px solid #EFEFF2 !important;
}
div[data-testid="stExpander"] details > summary::-webkit-details-marker { display: none !important; }
div[data-testid="stExpander"] details > summary:hover { background: #ECECEF !important; }

.sp-preview-injected {
    margin-left: auto !important;
    font-size: 14px !important;
    color: #8A8A93 !important;
    font-style: italic !important;
    max-width: 48% !important;
    overflow: hidden !important;
    text-overflow: ellipsis !important;
    white-space: nowrap !important;
    pointer-events: none !important;
    transition: opacity .2s ease !important;
}
div[data-testid="stExpander"] details[open] .sp-preview-injected { opacity: 0 !important; }

div[data-testid="stExpander"] details > div { background: #F2F2F4 !important; padding: 0 !important; }
div[data-testid="stExpander"] details > div > div { padding: 10px 18px 4px !important; }
div[data-testid="stExpander"] textarea {
    background: transparent !important;
    border: none !important;
    resize: none !important;
    font-size: 16px !important;
    line-height: 1.55 !important;
    color: #1A1A1F !important;
    font-family: inherit !important;
}
div[data-testid="stExpander"] textarea::placeholder { color: #8A8A93 !important; }

/* ── Chat input = BOTTOM half of unified card ───────────── */
div[data-testid="stBottom"] { padding: 0 !important; margin: 0 !important; right: 0 !important; }

div[data-testid="stChatInputContainer"] {
    background: #F2F2F4 !important;
    border: 1px solid #E6E6EA !important;
    border-top: none !important;
    border-radius: 0 0 14px 14px !important;
    padding: 10px 14px !important;
    margin: 0 !important;
}
div[data-testid="stChatInputContainer"] > div {
    padding: 0 !important;
    margin: 0 !important;
}
div[data-testid="stChatInputContainer"] textarea {
    background: transparent !important;
    font-size: 17px !important;
    color: #1A1A1F !important;
    padding-left: 0 !important;
    padding-right: 44px !important;
}
div[data-testid="stChatInputContainer"] textarea::placeholder { color: #8A8A93 !important; }
div[data-testid="stChatInputContainer"] button { border-radius: 9px !important; }

</style>
""", unsafe_allow_html=True)


# ─────────────────────────────────────────────────────────────
# SESSION STATE INIT
# ─────────────────────────────────────────────────────────────

for key, default in [
    ("conversations", {m: [] for m in MODELS}),
    ("history_page", 1),
    ("session_id", None),
    ("system_prompt", ""),
]:
    if key not in st.session_state:
        st.session_state[key] = default


# ─────────────────────────────────────────────────────────────
# API HELPERS
# ─────────────────────────────────────────────────────────────

def api_run(prompt, models, model_conversations=None, temperature=0.7, session_id=None, system_prompt=""):
    payload = {"prompt": prompt, "models": models, "temperature": temperature}
    if model_conversations:
        payload["model_conversations"] = model_conversations
    if session_id:
        payload["session_id"] = session_id
    if system_prompt:
        payload["system_prompt"] = system_prompt
    try:
        r = requests.post(f"{BASE_URL}/run", json=payload, timeout=60)
        r.raise_for_status()
        return r.json()
    except Exception:
        return None


def api_list_sessions(page=1, page_size=8):
    try:
        r = requests.get(f"{BASE_URL}/sessions", params={"page": page, "page_size": page_size}, timeout=10)
        r.raise_for_status()
        return r.json()
    except Exception:
        return None


def api_get_session(session_id):
    try:
        r = requests.get(f"{BASE_URL}/sessions/{session_id}", timeout=10)
        r.raise_for_status()
        return r.json()
    except Exception:
        return None


def api_delete_sessions(session_ids):
    try:
        r = requests.delete(f"{BASE_URL}/sessions", json={"session_ids": session_ids}, timeout=10)
        r.raise_for_status()
        return r.json()
    except Exception:
        return None


# ─────────────────────────────────────────────────────────────
# CORE: send a message to all selected models
# ─────────────────────────────────────────────────────────────

def send_message(user_text, selected_models, temperature, images=None):
    images = images or []

    if not st.session_state.session_id:
        st.session_state.session_id = str(uuid.uuid4())

    for model in selected_models:
        st.session_state.conversations[model].append(
            {"role": "user", "content": user_text, "images": images}
        )

    def _build_api_content(msg, model_name):
        imgs = msg.get("images") or []
        if imgs and not model_name.startswith("llama-groq"):
            content = [{"type": "text", "text": msg["content"]}]
            for img in imgs:
                url = img if (img.startswith("http") or img.startswith("data:")) \
                      else f"data:image/jpeg;base64,{img}"
                content.append({"type": "image_url", "image_url": {"url": url}})
            return content
        return msg["content"]

    model_convs = {
        m: [{"role": msg["role"], "content": _build_api_content(msg, m)}
            for msg in st.session_state.conversations[m]]
        for m in selected_models
    }

    with st.spinner("Getting responses from all models…"):
        result = api_run(user_text, selected_models, model_convs, temperature,
                         st.session_state.session_id, st.session_state.system_prompt)

    if result is None:
        for model in selected_models:
            if st.session_state.conversations[model]:
                st.session_state.conversations[model].pop()
        st.error("Backend unreachable. Make sure `uvicorn main:app --reload` is running on port 8000.")
        return False

    for r in result["results"]:
        content = r["response"] if r["success"] else f"⚠️ {r['error']}"
        st.session_state.conversations[r["model"]].append({
            "role": "assistant",
            "content": content,
            "latency_ms": r["latency_ms"],
            "total_tokens": r["total_tokens"],
            "cost_usd": r["cost_usd"],
            "success": r["success"],
        })
    return True


# ─────────────────────────────────────────────────────────────
# CONFIRM DELETE DIALOG
# ─────────────────────────────────────────────────────────────

@st.dialog("Confirm Deletion")
def confirm_delete_dialog(session_id):
    st.warning("Permanently delete this session? This cannot be undone.")
    col1, col2 = st.columns(2)
    with col1:
        if st.button("Yes, delete", type="primary", use_container_width=True):
            if api_delete_sessions([session_id]):
                if st.session_state.session_id == session_id:
                    st.session_state.conversations = {m: [] for m in MODELS}
                    st.session_state.session_id = None
                    st.session_state.system_prompt = ""
                st.rerun()
            else:
                st.error("Delete failed. Is the backend running?")
    with col2:
        if st.button("Cancel", use_container_width=True):
            st.rerun()


# ─────────────────────────────────────────────────────────────
# SIDEBAR
# ─────────────────────────────────────────────────────────────

with st.sidebar:
    st.title("⚡ LLM Platform")

    if st.button("＋ New Chat", use_container_width=True, type="primary"):
        st.session_state.conversations = {m: [] for m in MODELS}
        st.session_state.session_id = None
        st.session_state.system_prompt = ""
        st.rerun()

    st.divider()

    st.subheader("Models")
    selected_models = []
    for model in MODELS:
        if st.checkbox(model, value=True, key=f"chk_{model}"):
            selected_models.append(model)

    st.divider()

    st.subheader("Temperature")
    temperature = st.slider(
        label="temperature_slider",
        min_value=0.0,
        max_value=2.0,
        value=0.7,
        step=0.05,
        label_visibility="collapsed",
    )
    st.caption("0.0 = focused & deterministic   ·   1.0 = balanced   ·   2.0 = creative & random")

    st.divider()

    st.subheader("History")
    h1, h2 = st.columns(2)
    with h1:
        if st.button("◀", key="hist_prev"):
            if st.session_state.history_page > 1:
                st.session_state.history_page -= 1
    with h2:
        if st.button("▶", key="hist_next"):
            st.session_state.history_page += 1

    hist_data = api_list_sessions(page=st.session_state.history_page)
    if hist_data and hist_data.get("sessions"):
        for session in hist_data["sessions"]:
            turn_count = session["turn_count"]
            prompt_preview = session["first_prompt"]
            if len(prompt_preview) > 35:
                prompt_preview = prompt_preview[:35] + "…"
            label = (
                f"{prompt_preview}\n"
                f"{session['created_at'][:10]} · {turn_count} turn{'s' if turn_count != 1 else ''}"
            )
            col_btn, col_del = st.columns([0.70, 0.15])
            with col_btn:
                if st.button(label, key=f"session_{session['session_id']}", use_container_width=True):
                    detail = api_get_session(session["session_id"])
                    if detail:
                        new_convs = {m: [] for m in MODELS}
                        for turn in detail["turns"]:
                            for r in turn["results"]:
                                new_convs[r["model"]].append({
                                    "role": "user",
                                    "content": turn["prompt"],
                                    "system_prompt": turn.get("system_prompt"),
                                })
                                new_convs[r["model"]].append({
                                    "role": "assistant",
                                    "content": r["response"] if r["success"] else f"⚠️ {r['error']}",
                                    "latency_ms": r["latency_ms"],
                                    "total_tokens": r["total_tokens"],
                                    "cost_usd": r["cost_usd"],
                                    "success": r["success"],
                                })
                        st.session_state.conversations = new_convs
                        st.session_state.session_id = session["session_id"]
                        st.session_state.system_prompt = ""
                        st.rerun()
            with col_del:
                if st.button("🗑", key=f"del_{session['session_id']}"):
                    confirm_delete_dialog(session["session_id"])

        total_pages = hist_data.get("total_pages", 1)
        st.caption(f"Page {st.session_state.history_page} of {total_pages}")
    else:
        st.caption("No sessions yet.")


# ─────────────────────────────────────────────────────────────
# MAIN AREA
# ─────────────────────────────────────────────────────────────

if not selected_models:
    st.warning("Select at least one model from the sidebar.")
    st.stop()

max_msgs = max(
    (len(st.session_state.conversations.get(m, [])) for m in selected_models),
    default=0,
)

if max_msgs == 0:
    st.caption("Type a message below to start comparing models.")

model_cols = st.columns(len(selected_models))

for col, model in zip(model_cols, selected_models):
    model_conv = st.session_state.conversations.get(model, [])
    with col:
        st.markdown(f"**{model}**")
        with st.container(height=620, border=False):
            for i in range(0, len(model_conv), 2):
                with st.chat_message("user"):
                    if model_conv[i].get("system_prompt"):
                        st.caption(f"🔧 System: {model_conv[i]['system_prompt']}")
                    st.markdown(model_conv[i]["content"])
                    for _img in model_conv[i].get("images") or []:
                        st.image(_img, width=220)
                if i + 1 < len(model_conv):
                    r = model_conv[i + 1]
                    with st.chat_message("assistant"):
                        st.markdown(r["content"])
                        parts = []
                        if r.get("latency_ms"):
                            parts.append(f"⏱ {r['latency_ms']}ms")
                        if r.get("total_tokens"):
                            parts.append(f"🔢 {r['total_tokens']} tokens")
                        if r.get("cost_usd") is not None:
                            parts.append(f"💰 ${r['cost_usd']:.6f}")
                        if parts:
                            st.caption("  ·  ".join(parts))


# ─────────────────────────────────────────────────────────────
# SYSTEM PROMPT
# ─────────────────────────────────────────────────────────────

_sp_text = st.session_state.system_prompt
_preview = _sp_text[:80] + "…" if len(_sp_text) > 80 else _sp_text
st.markdown(
    f'<span id="sp-preview-cache" style="display:none">{_html.escape(_preview)}</span>',
    unsafe_allow_html=True,
)

with st.expander("⚙️ System Prompt", expanded=False):
    st.session_state.system_prompt = st.text_area(
        "system_prompt_input",
        value=st.session_state.system_prompt,
        placeholder="You are a helpful assistant...",
        height=100,
        label_visibility="collapsed",
    )


# ─────────────────────────────────────────────────────────────
# ALL JS IN ONE BLOCK — expander positioning + system-prompt preview
# ─────────────────────────────────────────────────────────────

components.html(f"""
<script>
(function () {{
    var D = window.parent.document;
    var W = window.parent;

    /* ── 1. Gear icon + preview into expander summary ── */
    function injectSummary() {{
        var summary = D.querySelector('[data-testid="stExpander"] details > summary');
        if (!summary || summary.querySelector('.sp-gear-injected')) return;
        var gear = D.createElement('span');
        gear.className = 'sp-gear-injected';
        gear.style.cssText = 'display:flex;color:#F0544B;flex:0 0 auto';
        gear.innerHTML = '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9z"></path></svg>';
        summary.insertBefore(gear, summary.firstChild);
        var details = summary.closest('details');
        if (details && !details._spBound) {{
            details._spBound = true;
            details.addEventListener('toggle', function () {{ setTimeout(sync, 50); }});
        }}
    }}

    function updatePreview() {{
        var cache = D.getElementById('sp-preview-cache');
        var summary = D.querySelector('[data-testid="stExpander"] details > summary');
        if (!cache || !summary) return;
        var prev = summary.querySelector('.sp-preview-injected');
        if (!prev) {{
            prev = D.createElement('span');
            prev.className = 'sp-preview-injected';
            summary.appendChild(prev);
        }}
        prev.textContent = cache.textContent;
    }}

    /* ── 2. Position everything ── */
    function positionAll() {{
        var stBot  = D.querySelector('[data-testid="stBottom"]');
        var exp    = D.querySelector('[data-testid="stExpander"]');
        if (!stBot) return;

        var br = stBot.getBoundingClientRect();
        var bh = Math.round(br.height);

        /* expander */
        if (exp) {{
            exp.style.left   = Math.round(br.left) + 'px';
            exp.style.right  = '0px';
            exp.style.bottom = bh + 'px';
        }}

        /* content padding */
        var eh  = exp ? Math.round(exp.getBoundingClientRect().height) : 0;
        var tag = D.getElementById('__sp_h');
        if (!tag) {{ tag = D.createElement('style'); tag.id = '__sp_h'; D.head.appendChild(tag); }}
        tag.textContent = 'div[data-testid="stMainBlockContainer"]{{padding-bottom:' + (eh + bh + 30) + 'px!important}}';
    }}

    function sync() {{
        injectSummary();
        updatePreview();
        positionAll();
    }}

    sync();
    [150, 400, 800, 1500].forEach(function(d) {{ setTimeout(sync, d); }});

    function attachObservers() {{
        var exp   = D.querySelector('[data-testid="stExpander"]');
        var stBot = D.querySelector('[data-testid="stBottom"]');
        if (exp   && !exp._ro)   {{ exp._ro   = new ResizeObserver(positionAll); exp._ro.observe(exp); }}
        if (stBot && !stBot._ro) {{ stBot._ro = new ResizeObserver(positionAll); stBot._ro.observe(stBot); }}
    }}
    attachObservers();
    setTimeout(attachObservers, 600);
    W.addEventListener('resize', positionAll);
}})();
</script>
""", height=0)


# ─────────────────────────────────────────────────────────────
# CHAT INPUT  (native file attachment via accept_file)
# ─────────────────────────────────────────────────────────────

user_input = st.chat_input("Message all selected models…", accept_file="multiple")

if user_input:
    _text = (user_input.text or "").strip()
    _imgs = []
    for _f in (user_input.files or []):
        _data = _b64.b64encode(_f.read()).decode()
        _imgs.append(f"data:{_f.type};base64,{_data}")
    if _text or _imgs:
        if send_message(_text, selected_models, temperature, images=_imgs):
            st.rerun()