import { Semaphore } from 'async-mutex';

import type { LocaleData } from './global_locale';
import { isLocaleLoaded, setLocale } from './global_locale';

const localeLoadingSemaphore = new Semaphore(1);

export async function loadLocale() {
  // eslint-disable-next-line @typescript-eslint/prefer-nullish-coalescing -- we want to match empty strings
  const locale = document.querySelector<HTMLElement>('html')?.lang || 'en';

  // We use a Semaphore here so only one thing can try to load the locales at
  // the same time. If one tries to do it while its in progress, it will wait
  // for the initial load to finish before it is resumed (and will see that locale
  // data is already loaded)
  await localeLoadingSemaphore.runExclusive(async () => {
    // if the locale is already set, then do nothing
    if (isLocaleLoaded()) return;

    const localeModule = (await import(
      /* webpackMode: "lazy" */
      /* webpackChunkName: "locale/[request]" */
      /* webpackInclude: /\.json$/ */
      `mastodon/locales/${locale}.json`
    )) as { default?: LocaleData['messages'] } & LocaleData['messages'];

    const messages =
      localeModule.default && typeof localeModule.default === 'object'
        ? localeModule.default
        : localeModule;
    for (const [key, message] of Object.entries(messages)) {
      messages[key] = message
        .replaceAll(/mastodon ggmbh/gi, 'Team plusminus')
        .replaceAll(/mastodon gmbh/gi, 'Team plusminus')
        .replaceAll('Mastodon', 'Paon')
        .replaceAll('mastodon', 'paon')
        .replaceAll('マストドン', 'ぱおん');
    }

    setLocale({ messages, locale });
  });
}
