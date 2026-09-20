import PropTypes from 'prop-types';
import { useCallback, useState } from 'react';

import { defineMessages, useIntl, FormattedMessage } from 'react-intl';

import classNames from 'classnames';

import { useDispatch } from 'react-redux';

import AlternateEmailIcon from '@/material-icons/400-24px/alternate_email.svg?react';
import CampaignIcon from '@/material-icons/400-24px/campaign.svg?react';
import ReplyIcon from '@/material-icons/400-24px/reply.svg?react';
import VisibilityOffIcon from '@/material-icons/400-24px/visibility_off.svg?react';
import VolumeOffIcon from '@/material-icons/400-24px/volume_off.svg?react';
import { muteAccount } from 'mastodon/actions/accounts';
import { closeModal } from 'mastodon/actions/modal';
import Button from 'mastodon/components/button';
import { CheckBox } from 'mastodon/components/check_box';
import { Icon } from 'mastodon/components/icon';
import { RadioButton } from 'mastodon/components/radio_button';

const messages = defineMessages({
  hours: { id: 'intervals.full.hours', defaultMessage: '{number, plural, one {# hour} other {# hours}}' },
  days: { id: 'intervals.full.days', defaultMessage: '{number, plural, one {# day} other {# days}}' },
  indefinite: { id: 'mute_modal.indefinite', defaultMessage: 'Until I unmute them' },
  hideFromNotifications: { id: 'mute_modal.hide_from_notifications', defaultMessage: 'Hide from notifications' },
});

const RadioButtonLabel = ({ value, currentValue, onChange, label }) => (
  <RadioButton
    name='duration'
    value={value}
    checked={value === currentValue}
    onChange={onChange}
    label={label}
  />
);

RadioButtonLabel.propTypes = {
  value: PropTypes.string.isRequired,
  currentValue: PropTypes.string.isRequired,
  onChange: PropTypes.func.isRequired,
  label: PropTypes.node.isRequired,
};

export const MuteModal = ({ accountId, acct }) => {
  const intl = useIntl();
  const dispatch = useDispatch();
  const [notifications, setNotifications] = useState(true);
  const [muteDuration, setMuteDuration] = useState('0');
  const [expanded, setExpanded] = useState(false);

  const handleClick = useCallback(() => {
    dispatch(closeModal({ modalType: undefined, ignoreFocus: false }));
    dispatch(muteAccount(accountId, notifications, muteDuration));
  }, [dispatch, accountId, notifications, muteDuration]);

  const handleCancel = useCallback(() => {
    dispatch(closeModal({ modalType: undefined, ignoreFocus: false }));
  }, [dispatch]);

  const handleToggleNotifications = useCallback(({ target }) => {
    setNotifications(target.checked);
  }, []);

  const handleChangeMuteDuration = useCallback(({ target }) => {
    setMuteDuration(target.value);
  }, []);

  const handleToggleSettings = useCallback(() => {
    setExpanded(value => !value);
  }, []);

  return (
    <div className='modal-root__modal safety-action-modal'>
      <div className='safety-action-modal__top'>
        <div className='safety-action-modal__header'>
          <div className='safety-action-modal__header__icon'>
            <Icon id='volume-off' icon={VolumeOffIcon} />
          </div>

          <div>
            <h1><FormattedMessage id='mute_modal.title' defaultMessage='Mute user?' /></h1>
            <div>@{acct}</div>
          </div>
        </div>

        <div className='safety-action-modal__bullet-points'>
          <div>
            <div className='safety-action-modal__bullet-points__icon'><Icon id='campaign' icon={CampaignIcon} /></div>
            <div><FormattedMessage id='mute_modal.they_wont_know' defaultMessage="They won't know they've been muted." /></div>
          </div>

          <div>
            <div className='safety-action-modal__bullet-points__icon'><Icon id='eye-slash' icon={VisibilityOffIcon} /></div>
            <div><FormattedMessage id='mute_modal.you_wont_see_posts' defaultMessage="They can still see your posts, but you won't see theirs." /></div>
          </div>

          <div>
            <div className='safety-action-modal__bullet-points__icon'><Icon id='at' icon={AlternateEmailIcon} /></div>
            <div><FormattedMessage id='mute_modal.you_wont_see_mentions' defaultMessage="You won't see posts that mention them." /></div>
          </div>

          <div>
            <div className='safety-action-modal__bullet-points__icon'><Icon id='reply' icon={ReplyIcon} /></div>
            <div><FormattedMessage id='mute_modal.they_can_mention_and_follow' defaultMessage="They can mention and follow you, but you won't see them." /></div>
          </div>
        </div>
      </div>

      <div className={classNames('safety-action-modal__bottom', { active: expanded })}>
        <div className='safety-action-modal__bottom__collapsible'>
          <div className='safety-action-modal__field-group'>
            <RadioButtonLabel value='0' label={intl.formatMessage(messages.indefinite)} currentValue={muteDuration} onChange={handleChangeMuteDuration} />
            <RadioButtonLabel value='21600' label={intl.formatMessage(messages.hours, { number: 6 })} currentValue={muteDuration} onChange={handleChangeMuteDuration} />
            <RadioButtonLabel value='86400' label={intl.formatMessage(messages.hours, { number: 24 })} currentValue={muteDuration} onChange={handleChangeMuteDuration} />
            <RadioButtonLabel value='604800' label={intl.formatMessage(messages.days, { number: 7 })} currentValue={muteDuration} onChange={handleChangeMuteDuration} />
            <RadioButtonLabel value='2592000' label={intl.formatMessage(messages.days, { number: 30 })} currentValue={muteDuration} onChange={handleChangeMuteDuration} />
          </div>

          <div className='safety-action-modal__field-group'>
            <CheckBox label={intl.formatMessage(messages.hideFromNotifications)} checked={notifications} onChange={handleToggleNotifications} />
          </div>
        </div>

        <div className='safety-action-modal__actions'>
          <button onClick={handleToggleSettings} className='link-button' aria-expanded={expanded}>
            {expanded ? <FormattedMessage id='mute_modal.hide_options' defaultMessage='Hide options' /> : <FormattedMessage id='mute_modal.show_options' defaultMessage='Show options' />}
          </button>

          <div className='spacer' />

          <button onClick={handleCancel} className='link-button'>
            <FormattedMessage id='confirmation_modal.cancel' defaultMessage='Cancel' />
          </button>

          <Button onClick={handleClick} autoFocus>
            <FormattedMessage id='confirmations.mute.confirm' defaultMessage='Mute' />
          </Button>
        </div>
      </div>
    </div>
  );
};

MuteModal.propTypes = {
  accountId: PropTypes.string.isRequired,
  acct: PropTypes.string.isRequired,
};

export default MuteModal;
