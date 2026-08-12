export const canEmbedStatus = status => {
  if (!status || !['public', 'unlisted'].includes(status.get('visibility'))) {
    return false;
  }

  return status.getIn(['account', 'username']) === status.getIn(['account', 'acct']);
};

