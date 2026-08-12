import { IntlProvider } from 'react-intl';

import { render, screen } from '@testing-library/react';

import { SpoilerButton } from '../spoiler_button';

describe('<SpoilerButton />', () => {
  it('names the media filter that caused the media to be hidden', () => {
    render(
      <IntlProvider locale='en'>
        <SpoilerButton
          sensitive={false}
          matchedFilters={['Images', 'Spoilers']}
          onClick={jest.fn()}
        />
      </IntlProvider>,
    );

    expect(screen.getByText(/Matches filter/)).toHaveTextContent(
      'Matches filter “Images, Spoilers”',
    );
    expect(screen.getByText('Click to show')).toBeInTheDocument();
  });
});
