import { createIntl } from 'react-intl';

import { fromJS } from 'immutable';

import { render, screen } from '@testing-library/react';

import { MediaModal } from '../media_modal';

jest.mock('@/material-icons/400-24px/chevron_left.svg?react', () => () => <svg data-testid='chevron-left' />, { virtual: true });
jest.mock('@/material-icons/400-24px/chevron_right.svg?react', () => () => <svg data-testid='chevron-right' />, { virtual: true });
jest.mock('@/material-icons/400-24px/close.svg?react', () => () => <svg data-testid='close' />, { virtual: true });
// eslint-disable-next-line react/prop-types
jest.mock('react-swipeable-views', () => ({ children }) => <div>{children}</div>);

const intl = createIntl({ locale: 'en' });

describe('Mastodon 4.4 media modal navigation', () => {
  it('uses logical previous and next classes that can be mirrored in RTL', () => {
    render(
      <MediaModal
        media={fromJS([{ id: '1', type: 'unknown' }, { id: '2', type: 'unknown' }])}
        index={0}
        intl={intl}
        onClose={jest.fn()}
        onChangeBackgroundColor={jest.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Previous' })).toHaveClass('media-modal__nav--prev');
    expect(screen.getByRole('button', { name: 'Next' })).toHaveClass('media-modal__nav--next');
  });
});
