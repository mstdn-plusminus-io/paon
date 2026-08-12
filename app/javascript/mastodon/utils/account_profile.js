export const PROFILE_AVATAR_SIZE = 92;

export const shouldShowFamiliarFollowers = ({
  accountId,
  signedIn,
  suspended,
  hidden,
  currentAccountId,
}) => Boolean(
  signedIn &&
  accountId &&
  accountId !== currentAccountId &&
  !suspended &&
  !hidden
);

export const accountRelationshipTagKeys = (relationship, isSelf = false) => {
  if (!relationship || isSelf) {
    return [];
  }

  const tags = [];

  if (relationship.get('followed_by') && (relationship.get('following') || relationship.get('requested'))) {
    tags.push('mutual');
  } else if (relationship.get('followed_by')) {
    tags.push('followed_by');
  } else if (relationship.get('requested_by')) {
    tags.push('requested_by');
  }

  if (relationship.get('blocking')) {
    tags.push('blocking');
  }

  if (relationship.get('muting')) {
    tags.push('muting');
  }

  if (relationship.get('domain_blocking')) {
    tags.push('domain_blocking');
  }

  return tags;
};
