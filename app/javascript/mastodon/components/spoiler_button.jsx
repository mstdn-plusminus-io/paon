import PropTypes from 'prop-types';

import { FormattedMessage } from 'react-intl';

import classNames from 'classnames';

import ImmutablePropTypes from 'react-immutable-proptypes';

export const SpoilerButton = ({
  hidden = false,
  sensitive,
  uncached = false,
  matchedFilters,
  onClick,
}) => {
  let warning;
  let action;

  if (uncached) {
    warning = (
      <FormattedMessage
        id='status.uncached_media_warning'
        defaultMessage='Preview not available'
      />
    );
    action = (
      <FormattedMessage id='status.media.open' defaultMessage='Click to open' />
    );
  } else if (matchedFilters) {
    warning = (
      <FormattedMessage
        id='filter_warning.matches_filter'
        defaultMessage='Matches filter “<span>{title}</span>”'
        values={{
          title: matchedFilters.join(', '),
          span: chunks => <span className='filter-name'>{chunks}</span>,
        }}
      />
    );
    action = (
      <FormattedMessage id='status.media.show' defaultMessage='Click to show' />
    );
  } else if (sensitive) {
    warning = (
      <FormattedMessage
        id='status.sensitive_warning'
        defaultMessage='Sensitive content'
      />
    );
    action = (
      <FormattedMessage id='status.media.show' defaultMessage='Click to show' />
    );
  } else {
    warning = (
      <FormattedMessage
        id='status.media_hidden'
        defaultMessage='Media hidden'
      />
    );
    action = (
      <FormattedMessage id='status.media.show' defaultMessage='Click to show' />
    );
  }

  return (
    <div
      className={classNames('spoiler-button', {
        'spoiler-button--hidden': hidden,
        'spoiler-button--click-thru': uncached,
      })}
    >
      <button
        type='button'
        className='spoiler-button__overlay'
        onClick={onClick}
        disabled={uncached}
      >
        <span className='spoiler-button__overlay__label'>
          {warning}
          <span className='spoiler-button__overlay__action'>{action}</span>
        </span>
      </button>
    </div>
  );
};

SpoilerButton.propTypes = {
  hidden: PropTypes.bool,
  sensitive: PropTypes.bool,
  uncached: PropTypes.bool,
  matchedFilters: PropTypes.oneOfType([
    PropTypes.arrayOf(PropTypes.string),
    ImmutablePropTypes.list,
  ]),
  onClick: PropTypes.func.isRequired,
};
