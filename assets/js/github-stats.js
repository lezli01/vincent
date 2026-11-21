/*
  Star and fork counts on the header's GitHub link.

  The counts come from GitHub's public REST API, asked from the reader's
  browser, because the site cannot carry them itself: a release tag's pages
  are built once and reused from the deploy's cache until the tag moves, so
  a number baked in at build time would freeze on /latest/ the day that
  release was first built. The answer is kept in localStorage for an hour,
  so a reader moving between pages asks once, well inside the API's
  unauthenticated limit of 60 requests an hour per address. Anything that
  goes wrong — offline, rate-limited, storage refused — leaves the plain
  link it already is.
*/
(function () {
  'use strict';

  var link = document.querySelector('[data-github-repo]');
  if (!link || !window.fetch) return;
  var stats = link.querySelector('.github-stats');
  if (!stats) return;

  var repo = link.getAttribute('data-github-repo');
  var key = 'vincent-github-stats:' + repo;
  var maxAge = 60 * 60 * 1000;

  // 1234 → 1.2k, 12345 → 12.3k: the header has room for a few characters.
  function short(n) {
    if (n < 1000) return String(n);
    return (Math.floor(n / 100) / 10).toFixed(1).replace(/\.0$/, '') + 'k';
  }

  function show(data) {
    var stars = link.querySelector('[data-github-stars]');
    var forks = link.querySelector('[data-github-forks]');
    if (!stars || !forks) return;
    stars.textContent = short(data.stars);
    forks.textContent = short(data.forks);
    stars.parentNode.title = data.stars + ' stars';
    forks.parentNode.title = data.forks + ' forks';
    stats.hidden = false;
  }

  function valid(data) {
    return data && typeof data.stars === 'number' && typeof data.forks === 'number';
  }

  try {
    var cached = JSON.parse(window.localStorage.getItem(key) || 'null');
    if (valid(cached) && Date.now() - cached.at < maxAge) {
      show(cached);
      return;
    }
  } catch (e) { /* storage refused or corrupt: ask the API */ }

  window.fetch('https://api.github.com/repos/' + repo, {
    headers: { Accept: 'application/vnd.github+json' }
  }).then(function (response) {
    if (!response.ok) throw new Error('GitHub API ' + response.status);
    return response.json();
  }).then(function (body) {
    var data = { stars: body.stargazers_count, forks: body.forks_count, at: Date.now() };
    if (!valid(data)) return;
    show(data);
    try { window.localStorage.setItem(key, JSON.stringify(data)); } catch (e) { /* fine */ }
  }).catch(function () { /* keep the plain link */ });
}());
