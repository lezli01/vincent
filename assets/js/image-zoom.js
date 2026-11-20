/*
  Click-to-enlarge screenshots.

  The TUI screenshots are 2400–3600 px captures shown at the article's width,
  where their terminal text is too small to read. Clicking one (or Enter /
  Space on it) opens it in an overlay at up to the viewport's size; a click
  anywhere, Escape or the close button dismisses it and returns focus.

  Skipped: images already inside a link (badges) and the logo, which are not
  screenshots. The README centres its screenshots in <p align="center"> just
  as it centres the logo, so the logo is told apart by its file name.
*/
(function () {
  'use strict';

  var prose = document.querySelector('.prose');
  if (!prose) return;

  var images = Array.prototype.filter.call(prose.querySelectorAll('img'), function (img) {
    return !img.closest('a') && !/\/logo[^/]*\.png$/.test(img.getAttribute('src') || '');
  });
  if (!images.length) return;

  var overlay = document.createElement('div');
  overlay.className = 'image-zoom';
  overlay.hidden = true;
  overlay.setAttribute('role', 'dialog');
  overlay.setAttribute('aria-modal', 'true');
  overlay.setAttribute('aria-label', 'Enlarged image');
  overlay.innerHTML =
    '<button type="button" class="image-zoom__close" aria-label="Close enlarged image">esc</button>' +
    '<figure class="image-zoom__figure"><img alt=""><figcaption class="image-zoom__caption"></figcaption></figure>';
  document.body.appendChild(overlay);

  var big = overlay.querySelector('img');
  var caption = overlay.querySelector('.image-zoom__caption');
  var closeButton = overlay.querySelector('.image-zoom__close');
  var opener = null;

  function open(img) {
    opener = img;
    big.src = img.currentSrc || img.src;
    big.alt = img.alt || '';
    caption.textContent = img.alt || '';
    caption.hidden = !img.alt;
    overlay.hidden = false;
    document.documentElement.classList.add('image-zoom-open');
    closeButton.focus();
  }

  function close() {
    if (overlay.hidden) return;
    overlay.hidden = true;
    document.documentElement.classList.remove('image-zoom-open');
    big.removeAttribute('src');
    if (opener) opener.focus();
    opener = null;
  }

  images.forEach(function (img) {
    img.classList.add('is-zoomable');
    img.tabIndex = 0;
    img.setAttribute('role', 'button');
    img.setAttribute('aria-label', 'Enlarge image' + (img.alt ? ': ' + img.alt : ''));
    img.addEventListener('click', function () { open(img); });
    img.addEventListener('keydown', function (event) {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        open(img);
      }
    });
  });

  overlay.addEventListener('click', close);
  document.addEventListener('keydown', function (event) {
    if (overlay.hidden) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      close();
    } else if (event.key === 'Tab') {
      // The close button is the dialog's only control: keep focus on it.
      event.preventDefault();
      closeButton.focus();
    }
  });
}());
