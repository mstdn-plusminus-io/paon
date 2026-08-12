import { IntlProvider } from 'react-intl';

import { fireEvent, render, screen } from '@testing-library/react';

import { QuoteError } from '../quote';

jest.mock('../status_content', () => () => null);
jest.mock('../media_attachments', () => () => null);
jest.mock('../../features/status/components/card', () => () => null);

const renderError = (state: string) =>
  render(
    <IntlProvider locale='en'>
      <QuoteError state={state} />
    </IntlProvider>,
  );

describe('<QuoteError />', () => {
  it.each([
    ['filtered', 'Hidden due to one of your filters'],
    ['deleted', 'This post was removed by its author.'],
    ['soft_deleted', 'This post was removed by its author.'],
    [
      'unauthorized',
      'This post cannot be displayed as you are not authorized to view it.',
    ],
    ['pending', 'This post is pending approval from the original author.'],
    [
      'rejected',
      'This post cannot be displayed as the original author does not allow it to be quoted.',
    ],
    [
      'revoked',
      'This post cannot be displayed as the original author does not allow it to be quoted.',
    ],
    ['not_found', 'This post cannot be displayed.'],
  ])('renders the %s lifecycle state', (state, message) => {
    renderError(state);

    expect(screen.getByText(message)).toBeInTheDocument();
  });

  it('lets a user reveal a limited quoted account', () => {
    const onRevealAccount = jest.fn();

    render(
      <IntlProvider locale='en'>
        <QuoteError
          accountId='42'
          onRevealAccount={onRevealAccount}
          state='limited'
        />
      </IntlProvider>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Show anyway' }));

    expect(onRevealAccount).toHaveBeenCalledWith('42');
  });

  it('renders a loading state before a shallow quote is fetched', () => {
    render(
      <IntlProvider locale='en'>
        <QuoteError isLoading state='not_found' />
      </IntlProvider>,
    );

    expect(screen.getByText('Loading quoted post…')).toBeInTheDocument();
  });
});
