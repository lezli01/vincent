/*
  Copy buttons on code blocks.

  Every <pre> in the prose gets a "copy" button in its top-right corner that
  puts the block's text on the clipboard. A block written as a transcript —
  some lines start with "$ ", the rest are the output — copies only the
  commands, without the prompt, because that is what a reader pastes into a
  shell. Without a clipboard (an insecure origin, an old browser) no button
  is added at all: selecting the text still works, so nothing is lost.

  The button lives on a wrapper rather than on the <pre> itself: the <pre>
  scrolls horizontally, and a button inside it would scroll away with the
  text.
*/
(function () {
  'use strict';

  var prose = document.querySelector('.prose');
  if (!prose) return;
  if (!(navigator.clipboard && navigator.clipboard.writeText)) return;

  var blocks = prose.querySelectorAll('pre');
  if (!blocks.length) return;

  var live = document.createElement('div');
  live.className = 'visually-hidden';
  live.setAttribute('role', 'status');
  live.setAttribute('aria-live', 'polite');
  document.body.appendChild(live);

  // The text a reader means to paste: the commands of a "$ " transcript —
  // with any lines a trailing backslash continues them onto — or the whole
  // block.
  function copyText(pre) {
    var text = (pre.textContent || '').replace(/\n+$/, '');
    if (!/^\$ /m.test(text)) return text;
    var commands = [];
    var continued = false;
    text.split('\n').forEach(function (line) {
      if (/^\$ /.test(line)) {
        commands.push(line.slice(2));
      } else if (continued) {
        commands.push(line);
      } else {
        return;
      }
      continued = /\\$/.test(line);
    });
    return commands.join('\n');
  }

  Array.prototype.forEach.call(blocks, function (pre) {
    // Rouge nests a fenced block as div.highlighter-rouge > div.highlight >
    // pre; wrap the outermost of those so the margins and the corner agree.
    var target = pre;
    var parent = pre.parentElement;
    if (parent && parent.classList.contains('highlight') && parent.parentElement &&
        parent.parentElement.classList.contains('highlighter-rouge')) {
      target = parent.parentElement;
    }

    var wrapper = document.createElement('div');
    wrapper.className = 'code-block';
    target.parentNode.insertBefore(wrapper, target);
    wrapper.appendChild(target);

    var button = document.createElement('button');
    button.type = 'button';
    button.className = 'code-copy';
    button.textContent = 'copy';
    button.setAttribute('aria-label', 'Copy code to clipboard');

    var timer = null;
    function settle(label, message) {
      window.clearTimeout(timer);
      button.textContent = label;
      button.classList.toggle('is-copied', label === 'copied');
      live.textContent = message;
      timer = window.setTimeout(function () {
        button.textContent = 'copy';
        button.classList.remove('is-copied');
      }, 1600);
    }

    button.addEventListener('click', function () {
      navigator.clipboard.writeText(copyText(pre)).then(function () {
        settle('copied', 'Code copied to clipboard');
      }, function () {
        settle('failed', 'Could not copy; select the text instead');
      });
    });

    wrapper.appendChild(button);
  });
}());
