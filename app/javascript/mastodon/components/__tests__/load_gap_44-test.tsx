import { IntlProvider } from 'react-intl';

import { fireEvent, render, screen } from '@testing-library/react';

import { LoadGap } from '../load_gap';

describe('Mastodon 4.4 timeline gap', () => {
  it('shows an accessible loading indicator after requesting more posts', () => {
    const onClick = jest.fn();

    render(
      <IntlProvider locale='en'>
        <LoadGap disabled={false} maxId='42' onClick={onClick} />
      </IntlProvider>,
    );

    const button = screen.getByRole('button', { name: 'Load more' });
    fireEvent.click(button);

    expect(onClick).toHaveBeenCalledWith('42');
    expect(button).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByLabelText('Loading…')).toBeInTheDocument();
  });
});
