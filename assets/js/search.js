/*
  Client-side documentation search (task 120).

  The index is built by /search.json at Jekyll build time — one record per
  section — and scored here. GitHub Pages cannot run a search plugin, and a
  hosted search service would send a local-first project's readers somewhere
  else, so the whole thing is one lazily fetched JSON file and this script.

  Record keys: p = page title, t = section title, u = link, x = section text.
*/
(function () {
  'use strict';

  var trigger = document.querySelector('[data-search-trigger]');
  var overlay = document.getElementById('search-overlay');
  var input = document.getElementById('search-input');
  var results = document.getElementById('search-results');
  var status = document.getElementById('search-status');
  if (!trigger || !overlay || !input || !results || !status) return;

  var MAX_RESULTS = 25;
  var SNIPPET = 170;

  var index = null;
  var loading = null;
  var active = -1;
  var lastFocus = null;
  var debounce = null;

  trigger.hidden = false;

  function escapeHtml(value) {
    return value.replace(/[&<>"']/g, function (ch) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch];
    });
  }

  function escapeRe(value) {
    return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }

  // Escapes and highlights in one pass: the marks are decided on the raw text,
  // so an escaped entity can never be split in half by a <mark>.
  function markUp(raw, tokens) {
    if (!tokens.length) return escapeHtml(raw);
    var re = new RegExp('(' + tokens.map(escapeRe).join('|') + ')', 'ig');
    var out = '';
    var last = 0;
    var match;
    while ((match = re.exec(raw)) !== null) {
      if (match.index === re.lastIndex) { re.lastIndex++; continue; }
      out += escapeHtml(raw.slice(last, match.index)) + '<mark>' + escapeHtml(match[0]) + '</mark>';
      last = match.index + match[0].length;
    }
    return out + escapeHtml(raw.slice(last));
  }

  function load() {
    if (loading) return loading;
    status.textContent = 'Loading the index…';
    loading = fetch(trigger.getAttribute('data-search-index'), { credentials: 'same-origin' })
      .then(function (response) {
        if (!response.ok) throw new Error('HTTP ' + response.status);
        return response.json();
      })
      .then(function (records) {
        index = records.map(function (record) {
          return {
            p: record.p, t: record.t, u: record.u, x: record.x,
            lp: (record.p || '').toLowerCase(),
            lt: (record.t || '').toLowerCase(),
            lx: (record.x || '').toLowerCase()
          };
        });
        return index;
      })
      .catch(function (err) {
        loading = null;
        status.textContent = 'Search is unavailable (' + err.message + '). Use GitHub search instead.';
        throw err;
      });
    return loading;
  }

  function score(record, tokens, phrase) {
    var total = 0;
    for (var i = 0; i < tokens.length; i++) {
      var token = tokens[i];
      var hit = 0;
      if (record.lt === token) hit += 140;
      else if (record.lt.indexOf(token) === 0) hit += 80;
      else if (record.lt.indexOf(token) !== -1) hit += 50;
      if (record.lp.indexOf(token) !== -1) hit += 10;
      var at = record.lx.indexOf(token);
      if (at !== -1) hit += 20 + Math.max(0, 10 - Math.floor(at / 200));
      if (hit === 0) return 0; // every token must appear somewhere
      total += hit;
    }
    if (tokens.length > 1) {
      if (record.lt.indexOf(phrase) !== -1) total += 90;
      else if (record.lx.indexOf(phrase) !== -1) total += 45;
    }
    return total;
  }

  function snippet(record, tokens) {
    var text = record.x || '';
    if (!text) return '';
    var at = -1;
    for (var i = 0; i < tokens.length && at === -1; i++) at = record.lx.indexOf(tokens[i]);
    if (at < 0) at = 0;
    var start = Math.max(0, at - 60);
    if (start > 0) {
      var space = text.indexOf(' ', start);
      if (space !== -1 && space - start < 20) start = space + 1;
    }
    var slice = text.slice(start, start + SNIPPET);
    return (start > 0 ? '…' : '') + slice + (start + SNIPPET < text.length ? '…' : '');
  }

  function render(query) {
    var tokens = query.toLowerCase().split(/\s+/).filter(Boolean);
    active = -1;
    input.setAttribute('aria-expanded', tokens.length ? 'true' : 'false');
    input.removeAttribute('aria-activedescendant');

    if (!tokens.length) {
      results.innerHTML = '';
      status.textContent = index
        ? index.length + ' sections indexed. Type to search.'
        : 'Loading the index…';
      return;
    }
    if (!index) return;

    var phrase = tokens.join(' ');
    var matches = [];
    for (var i = 0; i < index.length; i++) {
      var value = score(index[i], tokens, phrase);
      if (value > 0) matches.push({ record: index[i], score: value });
    }
    matches.sort(function (a, b) {
      if (b.score !== a.score) return b.score - a.score;
      return a.record.x.length - b.record.x.length; // a precise section beats a long one
    });

    var shown = matches.slice(0, MAX_RESULTS);
    status.textContent = matches.length === 0
      ? 'No matches for “' + query + '”.'
      : matches.length + (matches.length === 1 ? ' match' : ' matches') +
        (matches.length > shown.length ? ' — showing the first ' + shown.length + '.' : '');

    results.innerHTML = shown.map(function (entry, position) {
      var record = entry.record;
      var kicker = record.p && record.p !== record.t
        ? '<span class="search-result__page">' + escapeHtml(record.p) + '</span>'
        : '';
      return '<li role="option" aria-selected="false" id="search-result-' + position + '">' +
        '<a href="' + escapeHtml(record.u) + '">' +
        kicker +
        '<span class="search-result__title">' + markUp(record.t || record.p, tokens) + '</span>' +
        '<span class="search-result__snippet">' + markUp(snippet(record, tokens), tokens) + '</span>' +
        '</a></li>';
    }).join('');
  }

  function options() {
    return Array.prototype.slice.call(results.querySelectorAll('li'));
  }

  function select(next) {
    var items = options();
    if (!items.length) return;
    items.forEach(function (item) {
      item.setAttribute('aria-selected', 'false');
      item.classList.remove('is-active');
    });
    active = (next + items.length) % items.length;
    var current = items[active];
    current.setAttribute('aria-selected', 'true');
    current.classList.add('is-active');
    input.setAttribute('aria-activedescendant', current.id);
    if (current.scrollIntoView) current.scrollIntoView({ block: 'nearest' });
  }

  function go(link) {
    // Same-page anchors only change the hash, so the overlay has to be gone
    // first — and focus must not be pulled back to the trigger in the header,
    // which would scroll the reader away from the section they just chose.
    close(false);
    window.location.href = link.href;
  }

  function open() {
    if (!overlay.hidden) return;
    lastFocus = document.activeElement;
    overlay.hidden = false;
    document.documentElement.classList.add('search-open');
    trigger.setAttribute('aria-expanded', 'true');
    input.value = '';
    render('');
    input.focus();
    load().then(function () { render(input.value); }, function () {});
  }

  function close(restoreFocus) {
    if (overlay.hidden) return;
    overlay.hidden = true;
    document.documentElement.classList.remove('search-open');
    trigger.setAttribute('aria-expanded', 'false');
    if (restoreFocus !== false && lastFocus && lastFocus.focus) lastFocus.focus();
  }

  trigger.addEventListener('click', function (event) {
    event.preventDefault();
    open();
  });

  overlay.addEventListener('click', function (event) {
    if (event.target === overlay || event.target.hasAttribute('data-search-close')) {
      event.preventDefault();
      close();
    }
  });

  input.addEventListener('input', function () {
    var query = input.value;
    window.clearTimeout(debounce);
    debounce = window.setTimeout(function () { render(query); }, 70);
  });

  results.addEventListener('click', function (event) {
    var link = event.target.closest ? event.target.closest('a') : null;
    if (link) close(false); // the browser follows the link; the panel must not outlive it
  });

  results.addEventListener('mousemove', function (event) {
    var item = event.target.closest ? event.target.closest('li') : null;
    if (!item) return;
    var position = options().indexOf(item);
    if (position !== -1 && position !== active) select(position);
  });

  overlay.addEventListener('keydown', function (event) {
    if (event.key === 'Escape') {
      event.preventDefault();
      close();
      return;
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      select(active + 1);
      return;
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault();
      select(active === -1 ? -1 : active - 1);
      return;
    }
    if (event.key === 'Enter') {
      var items = options();
      var chosen = items[active === -1 ? 0 : active];
      var link = chosen && chosen.querySelector('a');
      if (link) {
        event.preventDefault();
        go(link);
      }
      return;
    }
    if (event.key === 'Tab') {
      var focusable = [input].concat(
        Array.prototype.slice.call(overlay.querySelectorAll('[data-search-close], .search-results a'))
      );
      var first = focusable[0];
      var last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
  });

  function isTyping(target) {
    if (!target || !target.tagName) return false;
    var tag = target.tagName.toLowerCase();
    return tag === 'input' || tag === 'textarea' || tag === 'select' || target.isContentEditable;
  }

  document.addEventListener('keydown', function (event) {
    if ((event.key === 'k' || event.key === 'K') && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      open();
      return;
    }
    if (event.key === '/' && !event.metaKey && !event.ctrlKey && !event.altKey && !isTyping(event.target)) {
      event.preventDefault();
      open();
    }
  });
}());
