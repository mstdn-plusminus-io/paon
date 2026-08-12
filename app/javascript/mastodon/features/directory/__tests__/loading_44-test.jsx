import { IntlProvider } from 'react-intl';

import { render, screen } from '@testing-library/react';

import { LoadMore } from 'mastodon/components/load_more';

import { directoryInitialLoad } from '../utils';

describe('Mastodon 4.4 directory loading', () => {
  it('replaces the list only during its initial load', () => {
    expect(directoryInitialLoad(true, 0)).toBe(true);
    expect(directoryInitialLoad(true, 10)).toBe(false);
  });

  it('keeps the load-more control in place with an inline spinner', () => {
    render(
      <IntlProvider locale='en'>
        <LoadMore onClick={jest.fn()} loading />
      </IntlProvider>,
    );

    expect(screen.getByRole('button')).toBeDisabled();
    expect(screen.getAllByRole('progressbar')).not.toHaveLength(0);
  });
});
