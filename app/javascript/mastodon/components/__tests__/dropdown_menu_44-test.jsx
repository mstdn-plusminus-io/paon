import { fireEvent, render, screen } from '@testing-library/react';

import Dropdown from '../dropdown_menu';

describe('Mastodon 4.4 dropdown accessibility', () => {
  it('exposes its controlled menu and restores focus when closing', () => {
    let dropdownId;
    const onOpen = jest.fn(id => {
      dropdownId = id;
    });
    const onClose = jest.fn();
    const props = {
      items: [{ text: 'Item' }],
      onOpen,
      onClose,
      openDropdownId: null,
    };
    const { rerender } = render(
      <Dropdown {...props}>
        <button type='button'>More</button>
      </Dropdown>,
    );
    const button = screen.getByRole('button', { name: 'More' });

    expect(button).toHaveAttribute('aria-expanded', 'false');
    expect(button).toHaveAttribute('aria-controls');
    fireEvent.click(button);
    expect(onOpen).toHaveBeenCalledWith(dropdownId, expect.any(Function), true);

    rerender(
      <Dropdown {...props} openDropdownId={dropdownId}>
        <button type='button'>More</button>
      </Dropdown>,
    );
    expect(button).toHaveAttribute('aria-expanded', 'true');

    fireEvent.click(button);
    expect(onClose).toHaveBeenCalledWith(dropdownId);
    expect(button).toHaveFocus();
  });
});
