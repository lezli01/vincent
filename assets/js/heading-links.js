/*
  Copyable heading links (task 120).

  kramdown already gives every heading an id, so the anchors exist; what was
  missing was a way to get one without reading the page source. Each heading
  gains a "#" affordance that copies the absolute link and, when the clipboard
  is refused or unavailable, degrades to the plain anchor it already is.
*/
(function () {
  'use strict';

  var prose = document.querySelector('.prose');
  if (!prose) return;

  var headings = prose.querySelectorAll('h2[id], h3[id], h4[id], h5[id], h6[id]');
  if (!headings.length) return;

  var canCopy = !!(navigator.clipboard && navigator.clipboard.writeText);

  var live = document.createElement('div');
  live.className = 'visually-hidden';
  live.setAttribute('role', 'status');
  live.setAttribute('aria-live', 'polite');
  document.body.appendChild(live);

  var timer = null;

  function flash(anchor, message) {
    window.clearTimeout(timer);
    prose.querySelectorAll('.heading-anchor.is-copied').forEach(function (other) {
      other.classList.remove('is-copied');
    });
    anchor.classList.add('is-copied');
    live.textContent = message;
    timer = window.setTimeout(function () {
      anchor.classList.remove('is-copied');
    }, 1600);
  }

  Array.prototype.forEach.call(headings, function (heading) {
    var label = (heading.textContent || '').trim();
    var anchor = document.createElement('a');
    anchor.className = 'heading-anchor';
    anchor.href = '#' + heading.id;
    anchor.setAttribute('aria-label', (canCopy ? 'Copy link to section: ' : 'Link to section: ') + label);
    anchor.innerHTML = '<span aria-hidden="true">#</span>';

    anchor.addEventListener('click', function (event) {
      if (!canCopy) return; // no clipboard: follow the anchor like any other link
      event.preventDefault();
      var link = window.location.origin + window.location.pathname + window.location.search + '#' + heading.id;
      navigator.clipboard.writeText(link).then(function () {
        // replaceState keeps the reader where they already are; a hash
        // assignment would re-scroll to the heading they just clicked.
        window.history.replaceState(null, '', '#' + heading.id);
        flash(anchor, 'Link copied: ' + link);
      }, function () {
        window.location.hash = heading.id;
      });
    });

    heading.appendChild(anchor);
  });
}());
