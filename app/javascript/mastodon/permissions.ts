export const PERMISSION_VIEW_FEEDS = 0x0000000000100000;
export const PERMISSION_INVITE_USERS = 0x0000000000010000;
export const PERMISSION_MANAGE_USERS = 0x0000000000000400;
export const PERMISSION_MANAGE_TAXONOMIES = 0x0000000000000100;
export const PERMISSION_MANAGE_FEDERATION = 0x0000000000000020;
export const PERMISSION_MANAGE_REPORTS = 0x0000000000000010;

export const canViewFeed = (
  signedIn: boolean,
  permissions: number,
  setting: 'public' | 'authenticated' | 'disabled' | undefined,
) => {
  switch (setting) {
    case 'public':
      return true;
    case 'authenticated':
      return signedIn;
    case 'disabled':
    default:
      return (permissions & PERMISSION_VIEW_FEEDS) === PERMISSION_VIEW_FEEDS;
  }
};
