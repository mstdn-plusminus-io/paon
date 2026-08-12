import mockPropTypes from 'prop-types';

import { createIntl, IntlProvider } from 'react-intl';

import { fromJS } from 'immutable';

import { fireEvent, render, screen } from '@testing-library/react';

import { PERMISSION_MANAGE_TAXONOMIES } from 'mastodon/permissions';

import { HashtagHeaderComponent } from '../hashtag_header';

jest.mock('mastodon/containers/dropdown_menu_container', () => {
  const MockDropdown = ({ items }) => (
    <div>
      {items.filter(Boolean).map(item => item.href ? (
        <a href={item.href} key={item.text}>{item.text}</a>
      ) : (
        <button key={item.text} type='button' onClick={item.action}>{item.text}</button>
      ))}
    </div>
  );

  MockDropdown.propTypes = {
    items: mockPropTypes.array.isRequired,
  };

  return MockDropdown;
});

const intl = createIntl({ locale: 'en' });
const tag = fromJS({
  id: '7',
  name: 'paon',
  following: false,
  featuring: false,
  history: [{ uses: '3', accounts: '2' }],
});

const renderHeader = props => render(
  <IntlProvider locale='en'>
    <HashtagHeaderComponent
      tag={tag}
      intl={intl}
      identity={{ signedIn: true, permissions: 0 }}
      onClick={jest.fn()}
      onFeature={jest.fn()}
      {...props}
    />
  </IntlProvider>,
);

describe('Mastodon 4.4 hashtag header actions', () => {
  it('features a hashtag from the retained header menu', () => {
    const onFeature = jest.fn();
    renderHeader({ onFeature });

    fireEvent.click(screen.getByRole('button', { name: 'Feature on profile' }));

    expect(onFeature).toHaveBeenCalledTimes(1);
  });

  it('shows the numeric moderation route only with taxonomy permission', () => {
    renderHeader({
      identity: {
        signedIn: true,
        permissions: PERMISSION_MANAGE_TAXONOMIES,
      },
    });

    expect(screen.getByRole('link', {
      name: 'Open moderation interface for #paon',
    })).toHaveAttribute('href', '/admin/tags/7');
  });

  it('switches to the unfeature label from reducer state', () => {
    renderHeader({ tag: tag.set('featuring', true) });

    expect(screen.getByRole('button', {
      name: "Don't feature on profile",
    })).toBeInTheDocument();
  });
});
