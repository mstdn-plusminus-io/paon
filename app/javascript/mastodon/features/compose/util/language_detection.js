import lande from 'lande';
import { debounce } from 'lodash';

import { urlRegex } from './url_regex';

const ISO_639_MAP = {
  afr: 'af',
  ara: 'ar',
  aze: 'az',
  bel: 'be',
  ben: 'bn',
  bul: 'bg',
  cat: 'ca',
  ces: 'cs',
  ckb: 'ku',
  cmn: 'zh',
  dan: 'da',
  deu: 'de',
  ell: 'el',
  eng: 'en',
  est: 'et',
  eus: 'eu',
  fin: 'fi',
  fra: 'fr',
  hau: 'ha',
  heb: 'he',
  hin: 'hi',
  hrv: 'hr',
  hun: 'hu',
  hye: 'hy',
  ind: 'id',
  isl: 'is',
  ita: 'it',
  jpn: 'ja',
  kat: 'ka',
  kaz: 'kk',
  kor: 'ko',
  lit: 'lt',
  mar: 'mr',
  mkd: 'mk',
  nld: 'nl',
  nob: 'no',
  pes: 'fa',
  pol: 'pl',
  por: 'pt',
  ron: 'ro',
  run: 'rn',
  rus: 'ru',
  slk: 'sk',
  spa: 'es',
  srp: 'sr',
  swe: 'sv',
  tgl: 'tl',
  tur: 'tr',
  ukr: 'uk',
  vie: 'vi',
};

export const guessLanguage = text => {
  const plainText = text
    .replace(urlRegex, '')
    .replace(/(^|[^/\w])@(([a-z0-9_]+)@[a-z0-9.-]+[a-z0-9]+)/gi, '');

  if (plainText.length > 20) {
    const [lang, confidence] = lande(plainText)[0];

    if (confidence > 0.8) {
      return ISO_639_MAP[lang] ?? '';
    }
  }

  return '';
};

export const debouncedGuess = debounce((text, setGuess) => {
  setGuess(guessLanguage(text));
}, 500, { maxWait: 1500, leading: true, trailing: true });

export const displayLanguageName = (locale, code, fallback) => {
  try {
    return new Intl.DisplayNames([locale], { type: 'language' }).of(code) || fallback;
  } catch {
    return fallback;
  }
};
