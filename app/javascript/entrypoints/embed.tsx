import './public-path';

import { createRoot } from 'react-dom/client';

import { start } from '../mastodon/common';
import { Status } from '../mastodon/features/standalone/status';
import { loadPolyfills } from '../mastodon/polyfills';
import ready from '../mastodon/ready';

start();

interface SetHeightRequest {
  type: 'setHeight';
  id: number | string;
}

const isSetHeightRequest = (data: unknown): data is SetHeightRequest => {
  if (!data || typeof data !== 'object') return false;

  const candidate = data as Partial<SetHeightRequest>;
  return (
    candidate.type === 'setHeight' &&
    (typeof candidate.id === 'number' || typeof candidate.id === 'string')
  );
};

let frameId: SetHeightRequest['id'] | null = null;
let heightFrame: number | null = null;

const sendHeight = () => {
  if (frameId === null) return;

  if (heightFrame !== null) window.cancelAnimationFrame(heightFrame);

  heightFrame = window.requestAnimationFrame(() => {
    heightFrame = null;
    const root = document.documentElement;
    const body = document.body;
    const height = Math.ceil(
      Math.max(
        root.scrollHeight,
        root.offsetHeight,
        body.scrollHeight,
        body.offsetHeight,
      ),
    );

    window.parent.postMessage({ type: 'setHeight', id: frameId, height }, '*');
  });
};

window.addEventListener('message', (event: MessageEvent<unknown>) => {
  if (event.source !== window.parent || !isSetHeightRequest(event.data)) return;

  frameId = event.data.id;
  sendHeight();
});

const observeHeight = (mountNode: HTMLElement) => {
  const resizeObserver =
    typeof ResizeObserver === 'undefined'
      ? null
      : new ResizeObserver(sendHeight);
  resizeObserver?.observe(mountNode);

  const mutationObserver = new MutationObserver(sendHeight);
  mutationObserver.observe(mountNode, {
    attributes: true,
    childList: true,
    characterData: true,
    subtree: true,
  });

  window.addEventListener('load', sendHeight);
  window.addEventListener('resize', sendHeight, { passive: true });
  document.addEventListener('toggle', sendHeight, true);
};

const loaded = () => {
  const mountNode = document.getElementById('mastodon-status');
  const encodedProps = mountNode?.getAttribute('data-props');

  if (!mountNode || !encodedProps) return;

  try {
    const props = JSON.parse(encodedProps) as { id?: unknown };

    if (typeof props.id !== 'string' || !/^\d+$/.test(props.id)) return;

    createRoot(mountNode).render(<Status id={props.id} />);
    observeHeight(mountNode);
  } catch (error: unknown) {
    console.error(error);
  }
};

const main = () => {
  ready(loaded).catch((error: unknown) => {
    console.error(error);
  });
};

loadPolyfills()
  .then(main)
  .catch((error: unknown) => {
    console.error(error);
  });
