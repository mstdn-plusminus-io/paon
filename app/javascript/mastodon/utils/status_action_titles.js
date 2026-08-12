import { defineMessages } from 'react-intl';

const messages = defineMessages({
  favourite: { id: 'status.favourite', defaultMessage: 'Favorite' },
  removeFavourite: { id: 'status.remove_favourite', defaultMessage: 'Remove from favorites' },
  bookmark: { id: 'status.bookmark', defaultMessage: 'Bookmark' },
  removeBookmark: { id: 'status.remove_bookmark', defaultMessage: 'Remove bookmark' },
});

export const favouriteActionTitle = (intl, active) =>
  intl.formatMessage(active ? messages.removeFavourite : messages.favourite);

export const bookmarkActionTitle = (intl, active) =>
  intl.formatMessage(active ? messages.removeBookmark : messages.bookmark);
