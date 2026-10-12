// Copyright (c) 2026 Lateralus Labs, LLC. Licensed under BSL 1.1.
const tools = document.querySelector('.journal-tools');
if (tools) {
  tools.hidden = false;
  const search = tools.querySelector('input');
  const filters = [...tools.querySelectorAll('[data-filter]')];
  const posts = [...document.querySelectorAll('[data-post]')];
  let topic = '';
  const applyFilters = () => {
    const query = search.value.trim().toLowerCase();
    let count = 0;
    posts.forEach(post => {
      post.hidden = !(post.dataset.search.includes(query) && (!topic || JSON.parse(post.dataset.tags).includes(topic)));
      if (!post.hidden) count++;
    });
    filters.forEach(button => button.setAttribute('aria-pressed', String(button.dataset.filter === topic)));
    document.querySelector('.search-status').textContent = `${count} ${count === 1 ? 'entry' : 'entries'} found`;
    document.querySelector('.empty-state').hidden = count !== 0;
  };
  search.addEventListener('input', applyFilters);
  filters.forEach(button => button.addEventListener('click', () => { topic = button.dataset.filter; applyFilters(); }));
  document.querySelector('[data-reset]').addEventListener('click', () => { topic = ''; search.value = ''; applyFilters(); search.focus(); });
}

const copy = document.querySelector('.copy-link');
if (copy && navigator.clipboard) {
  copy.hidden = false;
  copy.addEventListener('click', async () => {
    const status = document.querySelector('.copy-status');
    try {
      await navigator.clipboard.writeText(document.querySelector('link[rel="canonical"]').href);
      copy.textContent = 'Link copied ✓';
      status.textContent = 'Article link copied to clipboard.';
    } catch {
      status.textContent = 'Could not copy the link. Copy the address from your browser.';
      copy.textContent = 'Copy unavailable';
    }
  });
}

const progress = document.querySelector('.reading-progress');
if (progress) {
  const article = document.querySelector('main article');
  const headings = [...document.querySelectorAll('.article-body h2')];
  const links = [...document.querySelectorAll('.contents nav a')];
  const details = document.querySelector('.contents details');
  if (window.matchMedia('(max-width: 760px)').matches) details.open = false;
  progress.hidden = false;
  let scheduled = false;
  const update = () => {
    const top = article.getBoundingClientRect().top + window.scrollY;
    const distance = Math.max(1, article.offsetHeight - window.innerHeight);
    progress.value = Math.max(0, Math.min(100, (window.scrollY - top) / distance * 100));
    let current = null;
    for (const heading of headings) {
      if (heading.getBoundingClientRect().top <= 150) current = heading.id;
    }
    links.forEach(link => {
      if (link.hash === `#${current}`) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    });
    scheduled = false;
  };
  const schedule = () => { if (!scheduled) { scheduled = true; requestAnimationFrame(update); } };
  window.addEventListener('scroll', schedule, { passive: true });
  window.addEventListener('resize', schedule);
  document.fonts.ready.then(schedule);
  update();
}
