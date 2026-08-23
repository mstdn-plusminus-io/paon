import { fromJS } from 'immutable';

import { fireEvent, render, screen } from '@testing-library/react';

import Dropdown, { DropdownMenu } from '../dropdown_menu';

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
      <Dropdown {...props} openDropdownId={dropdownId} openedViaKeyboard>
        <button type='button'>More</button>
      </Dropdown>,
    );
    expect(button).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('button', { name: 'Item' })).toHaveFocus();

    fireEvent.click(button);
    expect(onClose).toHaveBeenCalledWith(dropdownId);
    expect(button).toHaveFocus();
  });

  it('focuses the first custom-rendered item when opened from the keyboard', () => {
    const renderItem = (item, index, { onClick }) => (
      <li key={item.text}>
        <button type='button' data-index={index} onClick={onClick}>{item.text}</button>
      </li>
    );

    render(
      <DropdownMenu
        items={[{ text: 'Revision' }]}
        openedViaKeyboard
        onClose={jest.fn()}
        onItemClick={jest.fn()}
        // eslint-disable-next-line react/jsx-no-bind -- The custom renderer is the behavior under test.
        renderItem={renderItem}
      />,
    );

    expect(screen.getByRole('button', { name: 'Revision' })).toHaveFocus();
  });

  it('keeps disabled items focusable without activating them', () => {
    let dropdownId;
    let onItemClick;
    const action = jest.fn();
    const props = {
      items: [{ text: 'Unavailable', disabled: true, action }],
      onOpen: (id, callback) => {
        dropdownId = id;
        onItemClick = callback;
      },
      onClose: jest.fn(),
      openDropdownId: null,
    };
    const { rerender } = render(
      <Dropdown {...props}>
        <button type='button'>More disabled</button>
      </Dropdown>,
    );

    fireEvent.click(screen.getByRole('button', { name: 'More disabled' }));
    rerender(
      <Dropdown {...props} openDropdownId={dropdownId} openedViaKeyboard>
        <button type='button'>More disabled</button>
      </Dropdown>,
    );

    const item = screen.getByRole('button', { name: 'Unavailable' });
    expect(item).toHaveAttribute('aria-disabled', 'true');
    expect(item).toHaveFocus();
    fireEvent.click(item);
    expect(action).not.toHaveBeenCalled();
    expect(props.onClose).not.toHaveBeenCalled();
    expect(onItemClick).toBeDefined();
  });

  it('highlights the current item and passes its click event to the action', () => {
    let dropdownId;
    const action = jest.fn();
    const props = {
      items: [{ text: 'Current policy', active: true, action }],
      onOpen: id => { dropdownId = id; },
      onClose: jest.fn(),
      openDropdownId: null,
    };
    const { rerender } = render(
      <Dropdown {...props}>
        <button type='button'>Quote policy</button>
      </Dropdown>,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Quote policy' }));
    rerender(
      <Dropdown {...props} openDropdownId={dropdownId}>
        <button type='button'>Quote policy</button>
      </Dropdown>,
    );

    const item = screen.getByRole('button', { name: 'Current policy' });
    expect(item.closest('li')).toHaveClass('dropdown-menu__item--highlighted');
    fireEvent.click(item);
    expect(action).toHaveBeenCalledWith(expect.any(Object));
  });

  it('keeps custom Immutable list items actionable', () => {
    let dropdownId;
    const onItemClick = jest.fn();
    const renderItem = (item, index, { onClick }) => (
      <li key={item.get('text')}>
        <button type='button' data-index={index} onClick={onClick}>{item.get('text')}</button>
      </li>
    );
    const props = {
      items: fromJS([{ text: 'Revision from list' }]),
      onOpen: id => { dropdownId = id; },
      onClose: jest.fn(),
      onItemClick,
      openDropdownId: null,
      renderItem,
    };
    const { rerender } = render(
      <Dropdown {...props}>
        <button type='button'>History</button>
      </Dropdown>,
    );

    fireEvent.click(screen.getByRole('button', { name: 'History' }));
    rerender(
      <Dropdown {...props} openDropdownId={dropdownId} openedViaKeyboard>
        <button type='button'>History</button>
      </Dropdown>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Revision from list' }));

    expect(onItemClick).toHaveBeenCalledWith(props.items.get(0), 0);
  });
});
