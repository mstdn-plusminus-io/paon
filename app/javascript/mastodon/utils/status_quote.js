export const isStatusQuoteable = (status, relationship, signedIn) => {
  if (!signedIn || !status) {
    return false;
  }

  const account = status.get('account');
  const unavailable = !account || account.get('suspended') || account.get('moved');
  const unavailableRelationship = relationship && (
    relationship.get('blocking') ||
    relationship.get('blocked_by') ||
    relationship.get('muting') ||
    relationship.get('domain_blocking')
  );

  return (
    ['public', 'unlisted'].includes(status.get('visibility')) &&
    !status.get('reblog') &&
    !unavailable &&
    !unavailableRelationship
  );
};
