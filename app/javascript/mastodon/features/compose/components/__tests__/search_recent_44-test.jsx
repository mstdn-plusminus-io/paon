import { fireEvent, render, screen } from '@testing-library/react';

import { RecentSearchOption } from '../search';

describe('Mastodon 4.4 recent search menu', () => {
  it('does not nest the forget button inside another native button', () => {
    const action = jest.fn();
    const forget = jest.fn(event => event.stopPropagation());

    render(<RecentSearchOption label='paon' action={action} forget={forget} />);

    const option = screen.getByRole('button', { name: /paon/ });
    const nativeButton = option.querySelector('button');
    expect(option.tagName).toBe('DIV');
    expect(nativeButton).not.toBeNull();

    fireEvent.mouseDown(nativeButton);
    expect(forget).toHaveBeenCalledTimes(1);
    expect(action).not.toHaveBeenCalled();
  });
});
