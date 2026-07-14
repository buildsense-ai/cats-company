function useSample(card) {

  const text = card.dataset.prompt;

  if (!text) return;

  const input = document.getElementById('input');

  input.value = text;

  autoGrow(input);

  updateSendBtn();

  input.focus();

}

function onSearchInput() {

  const input = document.getElementById('searchInput');

  const clearBtn = document.getElementById('searchClear');

  const query = input.value.trim();

  if (clearBtn) clearBtn.classList.toggle('show', query.length > 0);

  if (!query) { clearSearch(); return; }

  performSearch(query);

}

function clearSearch() {

  const input = document.getElementById('searchInput');

  if (input) input.value = '';

  const clearBtn = document.getElementById('searchClear');

  if (clearBtn) clearBtn.classList.remove('show');

  const results = document.getElementById('searchResults');

  if (results) { results.style.display = 'none'; results.innerHTML = ''; }

  document.querySelectorAll('.sidebar-section').forEach(s => s.style.display = '');

}

function highlightMatch(text, query) {

  if (!query) return escapeHtml(text);

  const re = new RegExp(escapeReg(query), 'gi');

  let result = '', last = 0, m;

  while ((m = re.exec(text)) !== null) {

    result += escapeHtml(text.slice(last, m.index));

    result += '<mark>' + escapeHtml(m[0]) + '</mark>';

    last = m.index + m[0].length;

    if (m.index === re.lastIndex) re.lastIndex++;

  }

  result += escapeHtml(text.slice(last));

  return result;

}

function performSearch(query) {

  const results = document.getElementById('searchResults');

  if (!results) return;

  const q = query.toLowerCase();

  const items = [];

  for (const s of state.sessions) {

    const title = (s.title || '').toLowerCase();

    const titleHit = title.includes(q);

    const messageHits = [];

    (s.messages || []).forEach((m, idx) => {

      if ((m.content || '').toLowerCase().includes(q)) messageHits.push({ idx, content: m.content, role: m.role });

    });

    if (titleHit || messageHits.length) items.push({ sess: s, titleHit, messageHits });

  }

  results.innerHTML = '';

  if (items.length === 0) {

    const e = document.createElement('div');

    e.className = 'search-empty';

    e.textContent = '暂无匹配';

    results.appendChild(e);

    results.style.display = 'block';

    document.querySelectorAll('.sidebar-section').forEach(s => s.style.display = 'none');

    return;

  }

  const MAX = 50; let count = 0;

  for (const item of items) {

    if (count >= MAX) break;

    const s = item.sess;

    const div = document.createElement('div');

    div.className = 'search-result-item';

    const titleHtml = highlightMatch(s.title || '新对话', query);

    let html = '<div class="result-title">对话: ' + titleHtml + '</div>';

    if (item.messageHits.length) {

      const m = item.messageHits[0];

      const lower = m.content.toLowerCase();

      const pos = lower.indexOf(q);

      const start = Math.max(0, pos - 30);

      const end = Math.min(m.content.length, pos + query.length + 30);

      let snippet = m.content.slice(start, end);

      if (start > 0) snippet = '...' + snippet;

      if (end < m.content.length) snippet = snippet + '...';

      const role = m.role === 'user' ? '我' : (m.role === 'error' ? '!' : 'AI');

      html += '<div class="result-snippet">[' + role + '] ' + highlightMatch(snippet, query) + '</div>';

    }

    div.innerHTML = html;

    div.onclick = () => {

      switchSession(s.id);

      if (item.messageHits.length) {

        setTimeout(() => {

          const firstMatch = item.messageHits[0];

          const allUserMsgs = document.querySelectorAll('.msg.user');

          const allBotMsgs = document.querySelectorAll('.msg.bot, .msg.bot.error');

          const idx = Math.floor(firstMatch.idx / 2);

          const target = firstMatch.role === 'user' ? allUserMsgs[idx] : allBotMsgs[idx];

          if (target) target.scrollIntoView({ behavior: 'smooth', block: 'center' });

        }, 100);

      }

    };

    results.appendChild(div); count++;

  }

  document.querySelectorAll('.sidebar-section').forEach(s => s.style.display = 'none');

  results.style.display = 'block';

}
