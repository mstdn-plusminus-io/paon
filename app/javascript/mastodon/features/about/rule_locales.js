export const ruleLocales = rules => {
  const locales = new Set();

  rules.forEach(rule => {
    rule.get('translations')?.keySeq().forEach(locale => locales.add(String(locale)));
  });

  return locales;
};

export const selectRuleLocale = (currentLocale, locales) => {
  if (currentLocale === 'default' || locales.has(currentLocale)) {
    return currentLocale;
  }

  const language = (currentLocale || '').split('-')[0];
  return Array.from(locales).find(locale => String(locale).split('-')[0] === language) ?? 'default';
};
