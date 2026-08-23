import { fromJS } from 'immutable';

import { ruleLocales, selectRuleLocale } from '../rule_locales';

describe('Mastodon 4.5 rule language selection', () => {
  it('hides the selector when no alternative translation exists', () => {
    const locales = ruleLocales(fromJS([{ id: '1', text: 'Be kind' }]));

    expect(locales.size).toBe(0);
  });

  it('selects exact and language-family translations before the default text', () => {
    const locales = ruleLocales(fromJS([{
      id: '1',
      translations: {
        'en-GB': { text: 'Be kind' },
        ja: { text: '親切に' },
      },
    }]));

    expect(selectRuleLocale('ja', locales)).toBe('ja');
    expect(selectRuleLocale('en-US', locales)).toBe('en-GB');
    expect(selectRuleLocale('fr', locales)).toBe('default');
  });
});
