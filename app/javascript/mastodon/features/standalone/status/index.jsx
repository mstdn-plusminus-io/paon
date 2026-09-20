import PropTypes from 'prop-types';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { Provider } from 'react-redux';

import { fetchStatus, hideStatus, revealStatus, translateStatus } from 'mastodon/actions/statuses';
import { hydrateStore } from 'mastodon/actions/store';
import { Router } from 'mastodon/components/router';
import DetailedStatus from 'mastodon/features/status/components/detailed_status';
import initialState from 'mastodon/initial_state';
import { IntlProvider } from 'mastodon/locales';
import { makeGetPictureInPicture, makeGetStatus } from 'mastodon/selectors';
import { store, useAppDispatch, useAppSelector } from 'mastodon/store';

const openAttachment = attachment => {
  const url = attachment?.get('url') || attachment?.get('remote_url');

  if (url) {
    window.open(url, '_blank', 'noopener,noreferrer');
  }
};

const Embed = ({ id }) => {
  const getStatus = useMemo(() => makeGetStatus(), []);
  const getPictureInPicture = useMemo(() => makeGetPictureInPicture(), []);
  const status = useAppSelector(state => getStatus(state, { id }));
  const pictureInPicture = useAppSelector(state => getPictureInPicture(state, { id }));
  const domain = useAppSelector(state => state.getIn(['meta', 'domain'], window.location.host));
  const dispatch = useAppDispatch();
  const [showMedia, setShowMedia] = useState(false);

  useEffect(() => {
    dispatch(fetchStatus(id, false, true));
  }, [dispatch, id]);

  useEffect(() => {
    if (status) {
      setShowMedia(!status.get('sensitive'));
    }
  }, [status]);

  const handleToggleHidden = useCallback(currentStatus => {
    dispatch(currentStatus.get('hidden') ? revealStatus(id) : hideStatus(id));
  }, [dispatch, id]);

  const handleTranslate = useCallback(currentStatus => {
    dispatch(translateStatus(currentStatus.get('id')));
  }, [dispatch]);

  const handleOpenMedia = useCallback((media, index) => {
    openAttachment(media?.get(index));
  }, []);

  const handleOpenVideo = useCallback(attachment => {
    openAttachment(attachment);
  }, []);

  const handleToggleMediaVisibility = useCallback(() => {
    setShowMedia(value => !value);
  }, []);

  return (
    <div className='embed'>
      <DetailedStatus
        status={status}
        domain={domain}
        pictureInPicture={pictureInPicture}
        onToggleHidden={handleToggleHidden}
        onTranslate={handleTranslate}
        onOpenMedia={handleOpenMedia}
        onOpenVideo={handleOpenVideo}
        onToggleMediaVisibility={handleToggleMediaVisibility}
        showMedia={showMedia}
      />
    </div>
  );
};

Embed.propTypes = {
  id: PropTypes.string.isRequired,
};

export const Status = ({ id }) => {
  const [hydrated, setHydrated] = useState(false);

  useEffect(() => {
    if (initialState) {
      store.dispatch(hydrateStore(initialState));
    }

    setHydrated(true);
  }, []);

  return (
    <IntlProvider>
      <Provider store={store}>
        <Router>
          {hydrated && <Embed id={id} />}
        </Router>
      </Provider>
    </IntlProvider>
  );
};

Status.propTypes = {
  id: PropTypes.string.isRequired,
};
