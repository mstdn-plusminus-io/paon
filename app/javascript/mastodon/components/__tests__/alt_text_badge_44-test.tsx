import { IntlProvider } from 'react-intl';

import { fireEvent, render, screen } from '@testing-library/react';

import { AltTextBadge } from '../alt_text_badge';

describe('Mastodon 4.4 alt text badge', () => {
  it('opens and can be dismissed by tapping the popover', () => {
    render(
      <IntlProvider locale='en'>
        <AltTextBadge description='A painted elephant' />
      </IntlProvider>,
    );

    const button = screen.getByRole('button', { name: 'ALT' });
    fireEvent.click(button);
    const popover = screen.getByRole('region');

    expect(button).toHaveAttribute('aria-expanded', 'true');
    fireEvent.mouseDown(popover, { clientX: 20, clientY: 20 });
    fireEvent.mouseUp(popover, {
      clientX: 20,
      clientY: 20,
      button: 0,
      detail: 1,
    });
    expect(button).toHaveAttribute('aria-expanded', 'false');
  });
});
