import PropTypes from 'prop-types';
import { useCallback, useEffect, useState } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import { useDispatch, useSelector } from 'react-redux';

import { updateAccount } from 'mastodon/actions/accounts';
import Button from 'mastodon/components/button';
import ColumnBackButton from 'mastodon/components/column_back_button';
import { Icon } from 'mastodon/components/icon';
import { LoadingIndicator } from 'mastodon/components/loading_indicator';
import { me } from 'mastodon/initial_state';
import { unescapeHTML } from 'mastodon/utils/html';

const messages = defineMessages({
  uploadHeader: { id: 'onboarding.profile.upload_header', defaultMessage: 'Upload profile header' },
  uploadAvatar: { id: 'onboarding.profile.upload_avatar', defaultMessage: 'Upload profile picture' },
});

const existingImage = path => path && !path.endsWith('missing.png') ? path : null;

const useImagePreview = (file, fallback) => {
  const [preview, setPreview] = useState(existingImage(fallback));
  useEffect(() => {
    if (!file) {
      setPreview(existingImage(fallback));
      return undefined;
    }
    const url = URL.createObjectURL(file);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [fallback, file]);
  return preview;
};

export const Profile = ({ onBack, onSaved, multiColumn }) => {
  const account = useSelector(state => state.getIn(['accounts', me]));
  const dispatch = useDispatch();
  const intl = useIntl();
  const [displayName, setDisplayName] = useState(account.get('display_name'));
  const [note, setNote] = useState(unescapeHTML(account.get('note')));
  const [avatar, setAvatar] = useState(null);
  const [header, setHeader] = useState(null);
  const [discoverable, setDiscoverable] = useState(!!account.get('discoverable', false));
  const [isSaving, setIsSaving] = useState(false);
  const [error, setError] = useState(false);
  const avatarPreview = useImagePreview(avatar, account.get('avatar'));
  const headerPreview = useImagePreview(header, account.get('header'));

  const handleHeaderChange = useCallback(event => {
    setHeader(event.target.files?.[0] || null);
  }, []);
  const handleAvatarChange = useCallback(event => {
    setAvatar(event.target.files?.[0] || null);
  }, []);
  const handleDisplayNameChange = useCallback(event => {
    setDisplayName(event.target.value);
  }, []);
  const handleNoteChange = useCallback(event => {
    setNote(event.target.value);
  }, []);
  const handleDiscoverableChange = useCallback(event => {
    setDiscoverable(event.target.checked);
  }, []);

  const handleSubmit = useCallback(() => {
    setIsSaving(true);
    setError(false);
    dispatch(updateAccount({ displayName, note, avatar, header, discoverable, indexable: discoverable }))
      .then(onSaved)
      .catch(() => {
        setIsSaving(false);
        setError(true);
      });
  }, [avatar, discoverable, dispatch, displayName, header, note, onSaved]);

  return (
    <>
      <ColumnBackButton multiColumn={multiColumn} onClick={onBack} />
      <div className='scrollable privacy-policy'>
        <div className='column-title'>
          <h3><FormattedMessage id='onboarding.profile.title' defaultMessage='Profile setup' /></h3>
          <p><FormattedMessage id='onboarding.profile.lead' defaultMessage='You can always complete this later in the settings, where even more customization options are available.' /></p>
        </div>

        <div className='onboarding__profile-form'>
          <div className='onboarding__profile-images'>
            <label className='onboarding__profile-header' title={intl.formatMessage(messages.uploadHeader)}>
              <input type='file' accept='image/*' onChange={handleHeaderChange} />
              {headerPreview ? <img src={headerPreview} alt='' /> : null}
              <span><Icon id={headerPreview ? 'pencil' : 'camera'} /> <FormattedMessage id='onboarding.profile.upload_header' defaultMessage='Upload profile header' /></span>
            </label>
            <label className='onboarding__profile-avatar' title={intl.formatMessage(messages.uploadAvatar)}>
              <input type='file' accept='image/*' onChange={handleAvatarChange} />
              {avatarPreview ? <img src={avatarPreview} alt='' /> : <Icon id='user' />}
            </label>
          </div>

          <label className='onboarding__profile-field'>
            <strong><FormattedMessage id='onboarding.profile.display_name' defaultMessage='Display name' /></strong>
            <span><FormattedMessage id='onboarding.profile.display_name_hint' defaultMessage='Your full name or your fun name…' /></span>
            <input type='text' value={displayName} onChange={handleDisplayNameChange} maxLength={30} />
          </label>
          <label className='onboarding__profile-field'>
            <strong><FormattedMessage id='onboarding.profile.note' defaultMessage='Bio' /></strong>
            <span><FormattedMessage id='onboarding.profile.note_hint' defaultMessage='You can @mention other people or #hashtags…' /></span>
            <textarea value={note} onChange={handleNoteChange} maxLength={500} rows={5} />
          </label>
          <label className='onboarding__profile-discoverable'>
            <input type='checkbox' checked={discoverable} onChange={handleDiscoverableChange} />
            <span><strong><FormattedMessage id='onboarding.profile.discoverable' defaultMessage='Make my profile discoverable' /></strong><small><FormattedMessage id='onboarding.profile.discoverable_hint' defaultMessage='Your posts may appear in search results and your profile may be suggested to people with similar interests.' /></small></span>
          </label>
          {error ? <p className='onboarding__profile-error' role='alert'><FormattedMessage id='onboarding.profile.save_error' defaultMessage='Your profile could not be saved. Check the selected images and try again.' /></p> : null}
          <Button block onClick={handleSubmit} disabled={isSaving}>{isSaving ? <LoadingIndicator /> : <FormattedMessage id='onboarding.profile.save_and_continue' defaultMessage='Save and continue' />}</Button>
        </div>
      </div>
    </>
  );
};

Profile.propTypes = {
  onBack: PropTypes.func.isRequired,
  onSaved: PropTypes.func.isRequired,
  multiColumn: PropTypes.bool,
};
