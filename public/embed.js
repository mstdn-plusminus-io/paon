// @ts-check

(function (allowedPrefixes) {
  'use strict';

  /**
   * @param {() => void} loaded
   */
  var ready = function (loaded) {
    if (document.readyState === 'complete') {
      loaded();
    } else {
      document.addEventListener('readystatechange', function () {
        if (document.readyState === 'complete') {
          loaded();
        }
      });
    }
  };

  ready(function () {
    /** @type {Map<number, HTMLQuoteElement | HTMLIFrameElement>} */
    var embeds = window.__paonEmbeds || new Map();
    window.__paonEmbeds = embeds;

    if (!window.__paonEmbedMessageListener) {
      window.__paonEmbedMessageListener = true;

      window.addEventListener('message', function (e) {
      var data = e.data || {};

      if (typeof data !== 'object' || data.type !== 'setHeight' || !embeds.has(data.id) || !Number.isFinite(data.height) || data.height < 0 || data.height > 100000) {
        return;
      }

      var embed = embeds.get(data.id);
      var iframe = embed instanceof HTMLIFrameElement ? embed : embed.querySelector('iframe');

      if (!iframe || iframe.contentWindow !== e.source) {
        return;
      }

      iframe.height = Math.ceil(data.height);

      if (embed instanceof HTMLQuoteElement) {
        var placeholder = embed.querySelector('a');
        if (placeholder) placeholder.remove();
      }
      });
    }

    document.querySelectorAll('iframe.mastodon-embed').forEach(function (iframe) {
      if (iframe.dataset.paonEmbedInitialized === 'true') return;
      iframe.dataset.paonEmbedInitialized = 'true';
      // select unique id for each iframe
      var id = 0, failCount = 0, idBuffer = new Uint32Array(1);
      while (id === 0 || embeds.has(id)) {
        id = crypto.getRandomValues(idBuffer)[0];
        failCount++;
        if (failCount > 100) {
          // give up and assign (easily guessable) unique number if getRandomValues is broken or no luck
          id = -(embeds.size + 1);
          break;
        }
      }

      embeds.set(id, iframe);

      iframe.allow = 'fullscreen';
      iframe.sandbox = 'allow-scripts allow-same-origin allow-popups';
      iframe.style.overflow = 'hidden';
      iframe.style.border = '0';
      iframe.style.display = 'block';
      iframe.style.maxWidth = '100%';

      iframe.onload = function () {
        iframe.contentWindow.postMessage({
          type: 'setHeight',
          id: id,
        }, '*');
      };

      iframe.onload();
    });

    document.querySelectorAll('blockquote.mastodon-embed').forEach(function (container) {
      if (container.dataset.paonEmbedInitialized === 'true') return;
      container.dataset.paonEmbedInitialized = 'true';

      var rawEmbedUrl = container.getAttribute('data-embed-url');
      var embedUrl;
      try {
        embedUrl = new URL(rawEmbedUrl || '');
      } catch (_) {
        return;
      }
      if (embedUrl.protocol !== 'https:' && embedUrl.protocol !== 'http:') return;
      if (allowedPrefixes.every(function (prefix) { return !embedUrl.toString().startsWith(prefix); })) return;

      var id = 0, failCount = 0, idBuffer = new Uint32Array(1);
      while (id === 0 || embeds.has(id)) {
        id = crypto.getRandomValues(idBuffer)[0];
        failCount++;
        if (failCount > 100) {
          id = -(embeds.size + 1);
          break;
        }
      }
      embeds.set(id, container);

      var iframe = document.createElement('iframe');
      iframe.src = embedUrl.toString();
      iframe.width = '100%';
      iframe.height = 0;
      iframe.allow = 'fullscreen';
      iframe.sandbox = 'allow-scripts allow-same-origin allow-popups';
      iframe.style.border = '0';
      iframe.style.overflow = 'hidden';
      iframe.style.display = 'block';
      iframe.style.width = '100%';
      iframe.onload = function () {
        iframe.contentWindow.postMessage({ type: 'setHeight', id: id }, '*');
      };
      container.appendChild(iframe);
    });
  });
})((document.currentScript && document.currentScript.tagName.toUpperCase() === 'SCRIPT' && document.currentScript.dataset.allowedPrefixes) ? document.currentScript.dataset.allowedPrefixes.split(' ') : []);
