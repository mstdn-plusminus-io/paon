import PropTypes from 'prop-types';
import { useCallback } from 'react';

import { defineMessages, injectIntl } from 'react-intl';

import { connect } from 'react-redux';

import FormatQuoteIcon from '@/material-icons/400-24px/format_quote.svg?react';
import { changeComposeQuotePolicy } from 'mastodon/actions/compose';
import { Icon } from 'mastodon/components/icon';

const messages = defineMessages({
  label: { id: 'privacy.quote.label', defaultMessage: 'Who can quote' },
  anyone: { id: 'privacy.quote.public', defaultMessage: 'Anyone' },
  followers: { id: 'privacy.quote.followers', defaultMessage: 'Your followers' },
  nobody: { id: 'privacy.quote.nobody', defaultMessage: 'Nobody' },
});

const mapStateToProps = state => ({
  value: state.getIn(['compose', 'quote_policy'], 'public'),
  visibility: state.getIn(['compose', 'privacy'], 'public'),
});

const QuotePolicySelector = ({ value, visibility, onChange, intl }) => {
  const disabled = ['private', 'direct'].includes(visibility);
  const selectedValue = disabled ? 'nobody' : value;
  const handleChange = useCallback(event => {
    onChange(event.target.value);
  }, [onChange]);

  return (
    <label className='compose-form__quote-policy'>
      <Icon id='quote-right' icon={FormatQuoteIcon} />
      <span className='sr-only'>{intl.formatMessage(messages.label)}</span>
      {/* eslint-disable-next-line jsx-a11y/no-onchange */}
      <select
        aria-label={intl.formatMessage(messages.label)}
        disabled={disabled}
        value={selectedValue}
        onChange={handleChange}
      >
        <option value='public'>{intl.formatMessage(messages.anyone)}</option>
        <option value='followers'>{intl.formatMessage(messages.followers)}</option>
        <option value='nobody'>{intl.formatMessage(messages.nobody)}</option>
      </select>
    </label>
  );
};

QuotePolicySelector.propTypes = {
  value: PropTypes.string.isRequired,
  visibility: PropTypes.string.isRequired,
  onChange: PropTypes.func.isRequired,
  intl: PropTypes.object.isRequired,
};

export default connect(mapStateToProps, { onChange: changeComposeQuotePolicy })(injectIntl(QuotePolicySelector));
