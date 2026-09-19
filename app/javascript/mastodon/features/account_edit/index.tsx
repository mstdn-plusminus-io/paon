import { useCallback, useEffect, useState } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import api, { apiRequest, apiRequestGet } from 'mastodon/api';
import Button from 'mastodon/components/button';
import Column from 'mastodon/features/ui/components/column';

interface ProfileJSON {
  avatar: string | null;
  avatar_static: string | null;
  header: string | null;
  header_static: string | null;
  display_name: string;
  note: string;
  avatar_description: string | null;
  header_description: string | null;
  fields: { name: string; value: string }[];
  locked: boolean;
  bot: boolean;
  discoverable: boolean | null;
  hide_collections: boolean | null;
  indexable: boolean;
  show_media: boolean;
  show_media_replies: boolean;
  show_featured: boolean;
  attribution_domains: string[];
}

type TextProfileKey =
  | 'display_name'
  | 'note'
  | 'avatar_description'
  | 'header_description';

type BooleanProfileKey =
  | 'locked'
  | 'bot'
  | 'discoverable'
  | 'hide_collections'
  | 'indexable'
  | 'show_media'
  | 'show_media_replies'
  | 'show_featured';

const toggleMessages = defineMessages({
  locked: { id: 'account.locked', defaultMessage: 'Require follow requests' },
  bot: { id: 'account.bot', defaultMessage: 'This is an automated account' },
  discoverable: {
    id: 'account.discoverable',
    defaultMessage: 'Show this profile in discovery',
  },
  hide_collections: {
    id: 'account.hide_collections',
    defaultMessage: 'Hide follows and followers',
  },
  indexable: {
    id: 'account.indexable',
    defaultMessage: 'Allow public posts in search',
  },
  show_media: {
    id: 'account.show_media',
    defaultMessage: 'Show media on the profile',
  },
  show_media_replies: {
    id: 'account.show_media_replies',
    defaultMessage: 'Show media from replies',
  },
  show_featured: {
    id: 'account.show_featured',
    defaultMessage: 'Show featured content',
  },
});

const toggleKeys: BooleanProfileKey[] = [
  'locked',
  'bot',
  'discoverable',
  'hide_collections',
  'indexable',
  'show_media',
  'show_media_replies',
  'show_featured',
];

export const AccountEdit: React.FC = () => {
  const intl = useIntl();
  const [profile, setProfile] = useState<ProfileJSON>();
  const [saving, setSaving] = useState(false);
  const [imageSaving, setImageSaving] = useState<'avatar' | 'header'>();
  const [saved, setSaved] = useState(false);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let active = true;
    apiRequestGet<ProfileJSON>('v1/profile')
      .then((value) => {
        if (active) setProfile(value);
        return undefined;
      })
      .catch(() => {
        if (active) setFailed(true);
        return undefined;
      });
    return () => {
      active = false;
    };
  }, []);

  const update = useCallback((key: TextProfileKey, value: string) => {
    setProfile((current) => current && { ...current, [key]: value });
    setSaved(false);
  }, []);
  const handleDisplayNameChange = useCallback<
    React.ChangeEventHandler<HTMLInputElement>
  >(
    (event) => {
      update('display_name', event.target.value);
    },
    [update],
  );
  const handleNoteChange = useCallback<
    React.ChangeEventHandler<HTMLTextAreaElement>
  >(
    (event) => {
      update('note', event.target.value);
    },
    [update],
  );
  const handleAvatarDescriptionChange = useCallback<
    React.ChangeEventHandler<HTMLTextAreaElement>
  >(
    (event) => {
      update('avatar_description', event.target.value);
    },
    [update],
  );
  const handleHeaderDescriptionChange = useCallback<
    React.ChangeEventHandler<HTMLTextAreaElement>
  >(
    (event) => {
      update('header_description', event.target.value);
    },
    [update],
  );
  const updateBoolean = useCallback(
    (key: BooleanProfileKey, value: boolean) => {
      setProfile((current) => current && { ...current, [key]: value });
      setSaved(false);
    },
    [],
  );
  const updateField = useCallback(
    (index: number, key: 'name' | 'value', value: string) => {
      setProfile((current) => {
        if (!current) return current;
        const fields = [...current.fields];
        while (fields.length <= index) fields.push({ name: '', value: '' });
        const currentField = fields[index] ?? { name: '', value: '' };
        fields[index] = { ...currentField, [key]: value };
        return { ...current, fields };
      });
      setSaved(false);
    },
    [],
  );
  const handleFieldChange = useCallback<
    React.ChangeEventHandler<HTMLInputElement>
  >(
    (event) => {
      const index = Number(event.currentTarget.dataset.index);
      const key = event.currentTarget.dataset.field;
      if (Number.isInteger(index) && (key === 'name' || key === 'value')) {
        updateField(index, key, event.currentTarget.value);
      }
    },
    [updateField],
  );
  const handleAttributionDomainsChange = useCallback<
    React.ChangeEventHandler<HTMLTextAreaElement>
  >((event) => {
    const attributionDomains = event.currentTarget.value
      .split(/\r?\n/)
      .map((value) => value.trim())
      .filter(Boolean);
    setProfile((current) =>
      current
        ? { ...current, attribution_domains: attributionDomains }
        : current,
    );
    setSaved(false);
  }, []);
  const handleBooleanChange = useCallback<
    React.ChangeEventHandler<HTMLInputElement>
  >(
    (event) => {
      updateBoolean(
        event.currentTarget.name as BooleanProfileKey,
        event.currentTarget.checked,
      );
    },
    [updateBoolean],
  );

  const uploadImage = useCallback(
    async (location: 'avatar' | 'header', file?: File) => {
      if (!profile || !file || imageSaving) return;
      setImageSaving(location);
      setFailed(false);
      setSaved(false);
      const formData = new FormData();
      formData.append(location, file);
      formData.append(
        `${location}_description`,
        profile[`${location}_description`] ?? '',
      );
      try {
        const { data } = await api().patch<ProfileJSON>(
          '/api/v1/profile',
          formData,
        );
        setProfile(data);
        setSaved(true);
      } catch {
        setFailed(true);
      } finally {
        setImageSaving(undefined);
      }
    },
    [imageSaving, profile],
  );

  const handleAvatarChange = useCallback<
    React.ChangeEventHandler<HTMLInputElement>
  >(
    (event) => {
      void uploadImage('avatar', event.currentTarget.files?.[0]);
    },
    [uploadImage],
  );
  const handleHeaderChange = useCallback<
    React.ChangeEventHandler<HTMLInputElement>
  >(
    (event) => {
      void uploadImage('header', event.currentTarget.files?.[0]);
    },
    [uploadImage],
  );

  const deleteImage = useCallback(
    async (location: 'avatar' | 'header') => {
      if (!profile || imageSaving) return;
      setImageSaving(location);
      setFailed(false);
      setSaved(false);
      try {
        if (location === 'avatar') {
          await api().delete('/api/v1/profile/avatar');
        } else {
          await api().delete('/api/v1/profile/header');
        }
        const value = await apiRequestGet<ProfileJSON>('v1/profile');
        setProfile(value);
        setSaved(true);
      } catch {
        setFailed(true);
      } finally {
        setImageSaving(undefined);
      }
    },
    [imageSaving, profile],
  );
  const handleDeleteAvatar = useCallback(() => {
    void deleteImage('avatar');
  }, [deleteImage]);
  const handleDeleteHeader = useCallback(() => {
    void deleteImage('header');
  }, [deleteImage]);

  const save = useCallback(async () => {
    if (!profile || saving) return;
    setSaving(true);
    setFailed(false);
    try {
      const value = await apiRequest<ProfileJSON>('PATCH', 'v1/profile', {
        data: {
          display_name: profile.display_name,
          note: profile.note,
          avatar_description: profile.avatar_description ?? '',
          header_description: profile.header_description ?? '',
          fields_attributes: profile.fields,
          locked: profile.locked,
          bot: profile.bot,
          discoverable: Boolean(profile.discoverable),
          hide_collections: Boolean(profile.hide_collections),
          indexable: profile.indexable,
          show_media: profile.show_media,
          show_media_replies: profile.show_media_replies,
          show_featured: profile.show_featured,
          attribution_domains: profile.attribution_domains,
        },
      });
      setProfile(value);
      setSaved(true);
    } catch {
      setFailed(true);
    } finally {
      setSaving(false);
    }
  }, [profile, saving]);

  return (
    <Column
      heading={intl.formatMessage({
        id: 'account.edit_profile',
        defaultMessage: 'Edit profile',
      })}
    >
      <div className='paon-feature-page'>
        <h1>
          <FormattedMessage
            id='account.edit_profile'
            defaultMessage='Edit profile'
          />
        </h1>
        {!profile && !failed && (
          <p>
            <FormattedMessage
              id='loading_indicator.label'
              defaultMessage='Loading…'
            />
          </p>
        )}
        {failed && (
          <p role='alert'>
            <FormattedMessage
              id='profile.error'
              defaultMessage='Your profile could not be saved.'
            />
          </p>
        )}
        {profile && (
          <div className='paon-feature-page__form paon-feature-page__form--stacked'>
            <fieldset>
              <legend>
                <FormattedMessage
                  id='account.header'
                  defaultMessage='Header image'
                />
              </legend>
              {profile.header_static && (
                <img
                  className='paon-feature-page__profile-header'
                  src={profile.header_static}
                  alt={profile.header_description ?? ''}
                />
              )}
              <input
                type='file'
                accept='image/png,image/jpeg,image/gif,image/webp'
                aria-label={intl.formatMessage({
                  id: 'account.header_upload',
                  defaultMessage: 'Upload header image',
                })}
                disabled={Boolean(imageSaving)}
                onChange={handleHeaderChange}
              />
              {profile.header && (
                <button
                  type='button'
                  className='button button-secondary'
                  disabled={Boolean(imageSaving)}
                  onClick={handleDeleteHeader}
                >
                  <FormattedMessage
                    id='account.header_delete'
                    defaultMessage='Delete header image'
                  />
                </button>
              )}
            </fieldset>
            <fieldset>
              <legend>
                <FormattedMessage
                  id='account.avatar'
                  defaultMessage='Profile picture'
                />
              </legend>
              {profile.avatar_static && (
                <img
                  className='paon-feature-page__profile-avatar'
                  src={profile.avatar_static}
                  alt={profile.avatar_description ?? ''}
                />
              )}
              <input
                type='file'
                accept='image/png,image/jpeg,image/gif,image/webp'
                aria-label={intl.formatMessage({
                  id: 'account.avatar_upload',
                  defaultMessage: 'Upload profile picture',
                })}
                disabled={Boolean(imageSaving)}
                onChange={handleAvatarChange}
              />
              {profile.avatar && (
                <button
                  type='button'
                  className='button button-secondary'
                  disabled={Boolean(imageSaving)}
                  onClick={handleDeleteAvatar}
                >
                  <FormattedMessage
                    id='account.avatar_delete'
                    defaultMessage='Delete profile picture'
                  />
                </button>
              )}
            </fieldset>
            <label>
              <FormattedMessage
                id='account.display_name'
                defaultMessage='Display name'
              />
              <input
                type='text'
                maxLength={40}
                value={profile.display_name}
                onChange={handleDisplayNameChange}
              />
            </label>
            <label>
              <FormattedMessage id='account.bio' defaultMessage='Bio' />
              <textarea
                maxLength={500}
                value={profile.note}
                onChange={handleNoteChange}
              />
            </label>
            <label>
              <FormattedMessage
                id='account.avatar_description'
                defaultMessage='Profile picture description'
              />
              <textarea
                maxLength={150}
                value={profile.avatar_description ?? ''}
                onChange={handleAvatarDescriptionChange}
              />
            </label>
            <label>
              <FormattedMessage
                id='account.header_description'
                defaultMessage='Header description'
              />
              <textarea
                maxLength={150}
                value={profile.header_description ?? ''}
                onChange={handleHeaderDescriptionChange}
              />
            </label>
            <fieldset>
              <legend>
                <FormattedMessage
                  id='account.profile_fields'
                  defaultMessage='Profile fields'
                />
              </legend>
              {Array.from({ length: 4 }, (_, index) => {
                const field = profile.fields[index] ?? { name: '', value: '' };
                return (
                  <div className='paon-feature-page__field-row' key={index}>
                    <input
                      type='text'
                      maxLength={255}
                      value={field.name}
                      aria-label={intl.formatMessage({
                        id: 'account.profile_field_name',
                        defaultMessage: 'Field name',
                      })}
                      data-index={index}
                      data-field='name'
                      onChange={handleFieldChange}
                    />
                    <input
                      type='text'
                      maxLength={255}
                      value={field.value}
                      aria-label={intl.formatMessage({
                        id: 'account.profile_field_value',
                        defaultMessage: 'Field value',
                      })}
                      data-index={index}
                      data-field='value'
                      onChange={handleFieldChange}
                    />
                  </div>
                );
              })}
            </fieldset>
            <label>
              <FormattedMessage
                id='account.attribution_domains'
                defaultMessage='Attribution domains (one per line)'
              />
              <textarea
                value={profile.attribution_domains.join('\n')}
                onChange={handleAttributionDomainsChange}
              />
            </label>
            {toggleKeys.map((key) => (
              <label key={key}>
                <input
                  type='checkbox'
                  name={key}
                  checked={Boolean(profile[key])}
                  onChange={handleBooleanChange}
                />
                {intl.formatMessage(toggleMessages[key])}
              </label>
            ))}
            <Button
              disabled={saving}
              text={intl.formatMessage({
                id: 'generic.save_changes',
                defaultMessage: 'Save changes',
              })}
              onClick={save}
            />
            {saved && (
              <p role='status'>
                <FormattedMessage id='generic.saved' defaultMessage='Saved' />
              </p>
            )}
          </div>
        )}
      </div>
    </Column>
  );
};

export default AccountEdit;
