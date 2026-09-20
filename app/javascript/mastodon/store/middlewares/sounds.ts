import { isAction } from '@reduxjs/toolkit';
import type { Middleware } from '@reduxjs/toolkit';

import ready from 'mastodon/ready';
import { assetHost } from 'mastodon/utils/config';

import type { RootState } from '..';

interface AudioSource {
  src: string;
  type: string;
}

const createAudio = (sources: AudioSource[]) => {
  const audio = new Audio();
  sources.forEach(({ type, src }) => {
    const source = document.createElement('source');
    source.type = type;
    source.src = src;
    audio.appendChild(source);
  });
  return audio;
};

const play = (audio: HTMLAudioElement) => {
  if (!audio.paused) {
    audio.pause();
    if (typeof audio.fastSeek === 'function') {
      audio.fastSeek(0);
    } else {
      audio.currentTime = 0;
    }
  }

  void audio.play();
};

export const soundsMiddleware = (): Middleware<unknown, RootState> => {
  const soundCache: Record<string, HTMLAudioElement> = {};

  void ready(() => {
    soundCache.boop = createAudio([
      {
        src: `${assetHost}/sounds/boop.ogg`,
        type: 'audio/ogg',
      },
      {
        src: `${assetHost}/sounds/boop.mp3`,
        type: 'audio/mpeg',
      },
    ]);
  });

  return () => (next) => (action) => {
    const sound =
      isAction(action) &&
      'meta' in action &&
      action.meta &&
      typeof action.meta === 'object' &&
      'sound' in action.meta &&
      typeof action.meta.sound === 'string'
        ? action.meta.sound
        : undefined;

    const audio = sound ? soundCache[sound] : undefined;
    if (audio) {
      play(audio);
    }

    return next(action);
  };
};
