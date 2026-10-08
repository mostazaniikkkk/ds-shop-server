// Small panel helpers: tabs, drop zone, filter, confirmations
// and "working..." buttons on slow forms.
document.addEventListener('DOMContentLoaded', () => {
  for (const tabs of document.querySelectorAll('[data-tabs]')) {
    const card = tabs.parentElement;
    tabs.addEventListener('click', (e) => {
      const b = e.target.closest('[data-tab]');
      if (!b) return;
      for (const x of tabs.querySelectorAll('[data-tab]')) x.classList.toggle('on', x === b);
      for (const body of card.querySelectorAll('[data-body]')) body.hidden = body.dataset.body !== b.dataset.tab;
    });
  }

  for (const drop of document.querySelectorAll('[data-drop]')) {
    const input = drop.querySelector('input[type=file]');
    const text = drop.querySelector('[data-drop-text]');
    const show = () => {
      const n = input.files.length;
      text.textContent = n === 0 ? text.dataset.empty : n === 1 ? input.files[0].name : n + ' archivos';
    };
    text.dataset.empty = text.textContent;
    input.addEventListener('change', show);
    for (const ev of ['dragenter', 'dragover']) drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.add('over'); });
    for (const ev of ['dragleave', 'drop']) drop.addEventListener(ev, () => drop.classList.remove('over'));
    drop.addEventListener('drop', (e) => { e.preventDefault(); input.files = e.dataTransfer.files; show(); });
  }

  for (const f of document.querySelectorAll('[data-filter]')) {
    const list = document.querySelector(f.dataset.filter);
    f.addEventListener('input', () => {
      const q = f.value.trim().toLowerCase();
      for (const row of list.children) row.hidden = q !== '' && !row.dataset.text.toLowerCase().includes(q);
    });
  }

  document.addEventListener('submit', (e) => {
    const form = e.target;
    const msg = form.dataset.confirm || (e.submitter && e.submitter.dataset.confirm);
    if (msg && !confirm(msg)) { e.preventDefault(); return; }
    const b = e.submitter;
    if (b && b.dataset.busy) { b.disabled = true; b.textContent = b.dataset.busy; }
  });
});
