import { createIntl, IntlProvider } from 'react-intl';

import { fromJS } from 'immutable';

import { fireEvent, render, screen } from '@testing-library/react';

import { AccountNote } from '../account_note';

const intl = createIntl({ locale: 'en' });
const account = fromJS({ id: '42' });

const renderNote = props => render(
  <IntlProvider locale='en'>
    <AccountNote account={account} intl={intl} onSave={jest.fn()} {...props} />
  </IntlProvider>,
);

describe('Mastodon 4.4 account personal note', () => {
  it('shows a loader instead of an editable textarea before the relationship loads', () => {
    renderNote({ value: undefined });

    expect(screen.getAllByRole('progressbar')).not.toHaveLength(0);
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
  });

  it('submits once on Ctrl+Enter by letting blur perform the save', () => {
    const onSave = jest.fn();
    renderNote({ value: '', onSave });
    const textarea = screen.getByRole('textbox');

    fireEvent.change(textarea, { target: { value: 'Private note' } });
    textarea.focus();
    fireEvent.keyDown(textarea, { key: 'Enter', keyCode: 13, ctrlKey: true });

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith('Private note');
  });
});
