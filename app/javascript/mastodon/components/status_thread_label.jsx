import PropTypes from 'prop-types';

import { FormattedMessage } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { connect } from 'react-redux';

import ReplyIcon from '@/material-icons/400-24px/reply.svg?react';

import { DisplayName } from './display_name';
import { Icon } from './icon';

const StatusThreadLabel = ({ accountId, inReplyToAccountId, inReplyToAccount }) => {
  let label;

  if (accountId === inReplyToAccountId) {
    label = <FormattedMessage id='status.continued_thread' defaultMessage='Continued thread' />;
  } else if (inReplyToAccount) {
    label = (
      <FormattedMessage
        id='status.replied_to'
        defaultMessage='Replied to {name}'
        values={{
          name: <a href={`/@${inReplyToAccount.get('acct')}`} className='status__display-name muted'><DisplayName account={inReplyToAccount} /></a>,
        }}
      />
    );
  } else {
    label = <FormattedMessage id='status.replied_in_thread' defaultMessage='Replied in thread' />;
  }

  return (
    <div className='status__prepend'>
      <div className='status__prepend__icon'><Icon id='reply' icon={ReplyIcon} /></div>
      <span>{label}</span>
    </div>
  );
};

StatusThreadLabel.propTypes = {
  accountId: PropTypes.string.isRequired,
  inReplyToAccountId: PropTypes.string.isRequired,
  inReplyToAccount: ImmutablePropTypes.map,
};

const mapStateToProps = (state, { inReplyToAccountId }) => ({
  inReplyToAccount: state.getIn(['accounts', inReplyToAccountId]),
});

export default connect(mapStateToProps)(StatusThreadLabel);
